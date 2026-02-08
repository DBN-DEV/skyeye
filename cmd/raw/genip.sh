#!/bin/bash

# Generate random public IP range with /24 mask
# Generate random public IP range with /24 mask, excluding private ranges and duplicates
function genip_once() {
    while true; do
        random_octet1=$((RANDOM % 255))
        random_octet2=$((RANDOM % 255))
        random_octet3=$((RANDOM % 255))
        
        # Skip private ranges
        if [[ $random_octet1 -eq 10 || 
            ($random_octet1 -eq 172 && $random_octet2 -ge 16 && $random_octet2 -le 31) || 
            ($random_octet1 -eq 192 && $random_octet2 -eq 168) ||
            ($random_octet1 -eq 198 && $random_octet2 -eq 18) ||
            ($random_octet1 -eq 100 && $random_octet2 -eq 64) ||
            ($random_octet1 -eq 169 && $random_octet2 -eq 254) ||
            $random_octet1 -eq 127 ]]; then
            continue
        fi
        
        ip_range="${random_octet1}.${random_octet2}.${random_octet3}.0/24"
        
        # Check if this range already exists in ipnets.txt
        if [ -f "$(dirname "$0")/ipnets.txt" ]; then
            if grep -q "^${ip_range}$" "$(dirname "$0")/ipnets.txt"; then
                continue
            fi
        fi
        
        # Record the new network range
        echo "$ip_range" >> "$(dirname "$0")/ipnets.txt"
        break
    done

    # Test reachable IPs using fping
    reachable_ips=$(fping -a -r 0 -t 1000 -i 0.1 -g $ip_range 2>/dev/null)

    # Count reachable IPs
    count=$(echo "$reachable_ips" | wc -w)

    # If more than 50, randomly select 50
    if [ $count -gt 50 ]; then
        reachable_ips=$(echo "$reachable_ips" | tr ' ' '\n' | shuf -n 50 | tr '\n' ' ')
    fi

    # Append to ips.txt and deduplicate
    echo "$reachable_ips" | tr ' ' '\n' >> $(dirname "$0")/ips.txt

    count=$(wc -l < $(dirname "$0")/ips.txt)
    echo "Added $(echo "$reachable_ips" | wc -w) reachable IPs (total unique: $count) to ips.txt"
}

# 检查入参，如果1号入参为空，则赋值为10
ntimes=${1:-10}
for ((i=0; i<$ntimes; i++)); do
    genip_once
done