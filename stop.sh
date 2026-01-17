#!/bin/bash

# Define colors
RED='\033[0;31m'
NC='\033[0m'

echo -e "${RED}🛑 Stopping Arbix Commander System (Docker Mode)...${NC}"

if ! command -v docker compose &> /dev/null; then
    echo "Error: docker compose could not be found."
    exit 1
fi

# Stop and remove containers
docker compose down

echo -e "${RED}✅ Arbix System Stopped & Containers Removed.${NC}"
