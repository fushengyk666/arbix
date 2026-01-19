package monitor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fushengyk/arbix/internal/config"
	"go.uber.org/zap"
)

// Notifier sends alerts to configured channels
type Notifier struct {
	cfg    *config.Config
	logger *zap.SugaredLogger
	client *http.Client
}

// NewNotifier creates a new notifier with resolved channels
func NewNotifier(cfg *config.Config, logger *zap.SugaredLogger) *Notifier {
	return &Notifier{
		cfg:    cfg,
		logger: logger,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// AlertPayload contains all data needed for notification
type AlertPayload struct {
	StrategyName string // strategy that triggered
	RuleName     string
	Window       string
	Symbol       string
	TriggerTime  time.Time

	BaseMarket  MarketInfo
	OtherMarket MarketInfo

	HasOther   bool
	HasFunding bool

	SpreadPct   float64
	FundingRate float64
	NextFunding int64

	Precision int // Price precision from exchangeInfo (-1 if not available)
}

type MarketInfo struct {
	Type       string // spot or future_perp
	PriceStart float64
	PriceEnd   float64
	ChangePct  float64
}

// Send dispatches the alert to the specified channels
func (n *Notifier) Send(payload AlertPayload, channelNames []string) {
	// Generate message content
	msg := n.formatMessage(payload)

	// Log to console
	fmt.Println("---------------------------------------------------")
	fmt.Println(msg)
	fmt.Println("---------------------------------------------------")

	// Send to each channel
	for _, chName := range channelNames {
		resolved := n.cfg.ResolveChannel(chName)
		if resolved == nil {
			n.logger.Warnf("Unsupported or unconfigured channel: %s", chName)
			continue // channel disabled or not configured
		}

		go func(ch *config.ResolvedChannel) {
			if err := n.sendToChannel(ch, msg); err != nil {
				n.logger.Errorf("Failed to send to %s (%s): %v", ch.Name, ch.Type, err)
			}
		}(resolved)
	}
}

func (n *Notifier) formatMessage(p AlertPayload) string {
	var sb strings.Builder

	// Format window: "1h0m0s" -> "60m", "5m0s" -> "5m", "1m0s" -> "1m"
	window := p.Window
	window = strings.TrimSuffix(window, "0s")
	window = strings.TrimSuffix(window, "0m")
	// Convert hours to minutes: "1h" -> "60m"
	if strings.HasSuffix(window, "h") {
		hours := strings.TrimSuffix(window, "h")
		if h, err := strconv.Atoi(hours); err == nil {
			window = fmt.Sprintf("%dm", h*60)
		}
	}

	// Header
	sb.WriteString(fmt.Sprintf("🚨 【%s】%s 价格波动 🚨\n", window, p.Symbol))

	// Determine arrow
	arrow := func(pct float64) string {
		if pct < 0 {
			return "↓"
		}
		return "↑"
	}

	// formatP uses precision from payload
	formatP := func(price float64) string {
		return formatPriceWithPrecision(price, p.Precision)
	}

	isSpot := p.BaseMarket.Type == "spot"
	isFutures := p.BaseMarket.Type == "future_perp"

	// Determine spot and futures data
	var spotStart, spotEnd, spotPct float64
	var futStart, futEnd, futPct float64
	hasSpot := false
	hasFutures := false

	if isSpot {
		hasSpot = true
		spotStart = p.BaseMarket.PriceStart
		spotEnd = p.BaseMarket.PriceEnd
		spotPct = p.BaseMarket.ChangePct

		if p.HasOther && p.OtherMarket.Type == "future_perp" {
			hasFutures = true
			futStart = p.OtherMarket.PriceStart
			futEnd = p.OtherMarket.PriceEnd
			futPct = p.OtherMarket.ChangePct
		}
	} else if isFutures {
		hasFutures = true
		futStart = p.BaseMarket.PriceStart
		futEnd = p.BaseMarket.PriceEnd
		futPct = p.BaseMarket.ChangePct

		if p.HasOther && p.OtherMarket.Type == "spot" {
			hasSpot = true
			spotStart = p.OtherMarket.PriceStart
			spotEnd = p.OtherMarket.PriceEnd
			spotPct = p.OtherMarket.ChangePct
		}
	}

	// Always show Spot first if available
	if hasSpot {
		sb.WriteString(fmt.Sprintf("💵 现货：%v->%v %s%.2f%%",
			formatP(spotStart), formatP(spotEnd),
			arrow(spotPct), valuesAbs(spotPct)))
	}

	// Then show Futures if available
	if hasFutures {
		if hasSpot {
			sb.WriteString("\n\n")
		}
		sb.WriteString(fmt.Sprintf("📝 合约：%v->%v %s%.2f%%",
			formatP(futStart), formatP(futEnd),
			arrow(futPct), valuesAbs(futPct)))

		if p.HasFunding {
			nextFund := time.UnixMilli(p.NextFunding).In(getLocation()).Format("15:04")
			sb.WriteString(fmt.Sprintf("\n💰 费率：%.4f%% | 结算：%s", p.FundingRate*100, nextFund))
		}
	}

	// Spread (only if both markets exist)
	if hasSpot && hasFutures {
		sb.WriteString(fmt.Sprintf("\n\n🔀 价差：%.2f%%", p.SpreadPct))
	}

	// Timestamp
	sb.WriteString(fmt.Sprintf("\n\n🕒 %s", p.TriggerTime.In(getLocation()).Format("2006-01-02 15:04:05")))

	return sb.String()
}

func getLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	return loc
}

// formatPriceWithPrecision formats price using known precision, or adaptive if precision is -1
func formatPriceWithPrecision(price float64, precision int) string {
	if price == 0 {
		return "N/A"
	}
	var result string
	if precision >= 0 {
		result = fmt.Sprintf("%.*f", precision, price)
	} else {
		// Fallback: adaptive precision
		if price < 0.0001 {
			result = fmt.Sprintf("%.8f", price)
		} else if price < 0.01 {
			result = fmt.Sprintf("%.6f", price)
		} else if price < 1 {
			result = fmt.Sprintf("%.5f", price)
		} else if price < 10 {
			result = fmt.Sprintf("%.4f", price)
		} else if price < 100 {
			result = fmt.Sprintf("%.3f", price)
		} else {
			result = fmt.Sprintf("%.2f", price)
		}
	}
	// Trim trailing zeros and unnecessary decimal point
	if strings.Contains(result, ".") {
		result = strings.TrimRight(result, "0")
		result = strings.TrimRight(result, ".")
	}
	return result
}

func valuesAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func (n *Notifier) sendToChannel(ch *config.ResolvedChannel, msg string) error {
	switch ch.Type {
	case "telegram":
		return n.sendTelegram(ch, msg)
	case "webhook":
		return n.sendWebhook(ch, msg)
	default:
		return fmt.Errorf("unknown channel type: %s", ch.Type)
	}
}

func (n *Notifier) sendTelegram(ch *config.ResolvedChannel, msg string) error {
	if ch.Token == "" || ch.ChatID == "" {
		n.logger.Warnf("Telegram channel %s not configured (missing token or chat_id)", ch.Name)
		return nil // not configured
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", ch.Token)

	body := map[string]interface{}{
		"chat_id": ch.ChatID,
		"text":    msg,
	}

	if ch.ThreadID != "" {
		body["message_thread_id"] = ch.ThreadID
	}

	jsonBody, _ := json.Marshal(body)
	n.logger.Infof("[Telegram] Sending to %s (ChatID: %s, ThreadID: %s)", ch.Name, ch.ChatID, ch.ThreadID)
	// Don't log full body if it contains sensitive info, but here it's just chat_id/text

	resp, err := n.client.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		n.logger.Errorf("[Telegram] Failed to send request: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		n.logger.Errorf("[Telegram] API Error: Status %s", resp.Status)
		// Try to read body for error details
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		n.logger.Errorf("[Telegram] Response Body: %s", buf.String())
		return fmt.Errorf("telegram api error: %s", resp.Status)
	}

	n.logger.Infof("[Telegram] Successfully sent to %s", ch.Name)
	return nil
}

func (n *Notifier) sendWebhook(ch *config.ResolvedChannel, msg string) error {
	if ch.URL == "" {
		n.logger.Warnf("Webhook channel %s missing URL", ch.Name)
		return nil
	}

	n.logger.Infof("[Webhook] Preparing to send to %s (%s %s)", ch.Name, ch.Method, ch.URL)

	// Escape message for JSON
	escapedMsg := strings.ReplaceAll(msg, "\\", "\\\\")
	escapedMsg = strings.ReplaceAll(escapedMsg, "\"", "\\\"")
	escapedMsg = strings.ReplaceAll(escapedMsg, "\n", "\\n")

	var req *http.Request
	var err error

	switch strings.ToUpper(ch.Method) {
	case "GET":
		// Replace {{.Message}} in URL
		finalURL := strings.ReplaceAll(ch.URL, "{{.Message}}", url.QueryEscape(msg))
		req, err = http.NewRequest("GET", finalURL, nil)

	default: // POST
		var bodyBytes []byte
		if ch.Body != "" {
			// Replace {{.Message}} in body template
			finalBody := strings.ReplaceAll(ch.Body, "{{.Message}}", escapedMsg)
			bodyBytes = []byte(finalBody)
		} else {
			// Default JSON body
			d := map[string]string{"text": msg}
			bodyBytes, _ = json.Marshal(d)
		}

		n.logger.Debugf("[Webhook] Body: %s", string(bodyBytes))
		req, err = http.NewRequest("POST", ch.URL, bytes.NewBuffer(bodyBytes))
	}

	if err != nil {
		n.logger.Errorf("[Webhook] Failed to create request: %v", err)
		return err
	}

	// Parse and apply custom headers
	if ch.Headers != "" {
		var headers map[string]string
		if json.Unmarshal([]byte(ch.Headers), &headers) == nil {
			for k, v := range headers {
				req.Header.Set(k, v)
			}
		}
	}

	if req.Header.Get("Content-Type") == "" && ch.Method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := n.client.Do(req)
	if err != nil {
		n.logger.Errorf("[Webhook] Failed to send request: %v", err)
		return err
	}
	defer resp.Body.Close()

	n.logger.Infof("[Webhook] Response %s from %s", resp.Status, ch.Name)

	if resp.StatusCode >= 400 {
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		n.logger.Errorf("[Webhook] Error Body: %s", buf.String())
		return fmt.Errorf("webhook error: %s", resp.Status)
	}
	return nil
}
