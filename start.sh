#!/bin/bash

# Define colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}� Starting Arbix Commander System (Docker Mode)...${NC}"

# Check if docker-compose is available
if ! command -v docker compose &> /dev/null; then
    echo "Error: docker compose could not be found."
    exit 1
fi

# Check for .env file
if [ ! -f .env ]; then
    echo -e "${BLUE}⚠️  No .env file found. Copying from .env.example...${NC}"
    if [ -f .env.example ]; then
        cp .env.example .env
        echo -e "${GREEN}✅ Created .env from template. Please edit it with your credentials!${NC}"
        # Optional: exit 1 to force user to edit it? Or just let them run with empty creds for testing?
        # Given user is testing, maybe just warn clearly.
        echo -e "${BLUE}ℹ️  Edit .env to configure Telegram/Webhook credentials.${NC}"
        sleep 2
    else
        echo "Error: .env.example not found!"
        exit 1
    fi
fi

echo -e "${BLUE}🚀 Building and Starting Containers...${NC}"
# Use --build to ensure latest code is used, -d for detached mode
docker compose up -d --build

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✅ System is fully operational!${NC}"
    echo -e "📄 Logs: Run 'docker-compose logs -f' to view live logs."
else
    echo -e "${RED}❌ Failed to start containers.${NC}"
fi
