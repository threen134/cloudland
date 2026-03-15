#!/bin/bash

# Cloudland Control Plane Gateway System Initialization Script (v2 - JSON Config)
# This script reads from regions_config.json to register regions and seed admin accounts.

BASE_URL="${BASE_URL:-http://localhost:8000/api/v1}"
CONFIG_FILE="${CONFIG_FILE:-scripts/regions_config.json}"

# From .env (default values)
ROOT_USER="${ROOT_USER:-root}"
ROOT_PASS="${ROOT_PASS:-root_password_change_me}"

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m' # No Color

if [ ! -f "$CONFIG_FILE" ]; then
    echo -e "${RED}Error: Configuration file $CONFIG_FILE not found.${NC}"
    exit 1
fi

echo -e "${BLUE}=== Cloudland Initialization Started (JSON Mode) ===${NC}"

# 1. Get Root Access Token
echo -e "Logging in as Root..."
RESPONSE=$(curl -s -X POST "${BASE_URL}/auth/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "username=${ROOT_USER}&password=${ROOT_PASS}")

TOKEN=$(echo $RESPONSE | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')

if [ -z "$TOKEN" ]; then
    echo -e "${RED}Error: Failed to get Access Token. Check ROOT_PASS and BASE_URL.${NC}"
    echo "Response: $RESPONSE"
    exit 1
fi

echo -e "${GREEN}Root Token Obtained.${NC}"

# Function to parse JSON using Python (since jq might not be available)
parse_json() {
    python3 -c "import json; v = json.load(open('$CONFIG_FILE')); print('\n'.join(['|'.join([str(r.get(k, '')) for k in ['name', 'endpoint_url', 'description', 'admin_username', 'admin_password', 'admin_email']]) for r in v]))"
}

# 2. Iterate through regions defined in JSON
REGIONS_RAW=$(parse_json)

while IFS='|' read -r NAME ENDPOINT DESC ADMIN_USER ADMIN_PASS ADMIN_EMAIL; do
    if [ -z "$NAME" ]; then continue; fi
    
    echo -e "\n${BLUE}--- Processing Region: ${NAME} ---${NC}"
    
    # A. Register Region
    echo "Registering region..."
    REGION_JSON=$(curl -s -X POST "${BASE_URL}/regions" \
      -H "Authorization: Bearer ${TOKEN}" \
      -H "Content-Type: application/json" \
      -d "{
        \"name\": \"${NAME}\",
        \"endpoint_url\": \"${ENDPOINT}\",
        \"description\": \"${DESC}\"
      }")
    
    if [[ $REGION_JSON == *"already exists"* ]]; then
        echo -e "Region ${NAME} already exists. Skipping registration..."
    elif [[ $REGION_JSON == *"uuid"* ]]; then
        echo -e "${GREEN}Region ${NAME} registered successfully.${NC}"
    else
        echo -e "${RED}Warning: Registration failed for ${NAME}.${NC}"
        echo "Response: $REGION_JSON"
    fi
    
    # B. Initialize Admin Account (Admin Seeding)
    echo "Seeding Admin account: ${ADMIN_USER} (${ADMIN_EMAIL:-no-email})..."
    SEED_DATA="{
        \"region\": \"${NAME}\",
        \"admin_account\": \"${ADMIN_USER}\",
        \"password\": \"${ADMIN_PASS}\""
    
    if [ -n "$ADMIN_EMAIL" ]; then
        SEED_DATA="${SEED_DATA}, \"email\": \"${ADMIN_EMAIL}\""
    fi
    SEED_DATA="${SEED_DATA} }"

    SEED_JSON=$(curl -s -X POST "${BASE_URL}/admin-accounts" \
      -H "Authorization: Bearer ${TOKEN}" \
      -H "Content-Type: application/json" \
      -d "$SEED_DATA")
      
    if [[ $SEED_JSON == *"uuid"* || $SEED_JSON == *"region"* ]]; then
        echo -e "${GREEN}Admin account for ${NAME} initialized.${NC}"
    else
        echo -e "${RED}Warning: Admin seeding failed for ${NAME}.${NC}"
        echo "Response: $SEED_JSON"
    fi

done <<< "$REGIONS_RAW"

echo -e "\n${BLUE}=== Initialization Complete ===${NC}"
