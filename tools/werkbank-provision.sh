#!/bin/bash
# werkbank — Workspace-Plattform (Coder + Verwandte).
# Eigene hcloud-Box, getrennt von werkstatt (Stay-Awesome-Apps) und den
# QuantBot-/Vault-Boxen. Idempotent — re-runnable, prüft vor jedem Schritt.
#
# Voraussetzungen auf der Aufruferseite:
#   - hcloud CLI authentifiziert (HCLOUD_TOKEN gesetzt)
#   - cf-dns CLI mit Cloudflare-Token
#   - SSH-Keys gastown-prod-ed25519 und mario@hetzner-all in hcloud hinterlegt
#
# Sapling: st-vmvnq

set -euo pipefail

SERVER_NAME="werkbank"
SERVER_TYPE="${SERVER_TYPE:-ccx33}"   # 8 vCPU dedicated, 32 GB RAM, 240 GB
SERVER_IMAGE="${SERVER_IMAGE:-ubuntu-24.04}"
SERVER_LOCATION="${SERVER_LOCATION:-nbg1}"
SSH_KEYS=(gastown-prod-ed25519 mario@hetzner-all)
FQDN="werkbank.stayawesome.app"
LE_EMAIL="${LE_EMAIL:-mario.gemuenden@stayawesome.de}"

echo "== Phase 1: hcloud-Server =="
if hcloud server list -o columns=name | grep -qx "$SERVER_NAME"; then
  echo "  ok: $SERVER_NAME existiert bereits"
else
  args=(--name "$SERVER_NAME" --type "$SERVER_TYPE" --image "$SERVER_IMAGE"
        --location "$SERVER_LOCATION"
        --label purpose=workspace-platform
        --label owner=stayawesome
        --label sapling=st-vmvnq)
  for k in "${SSH_KEYS[@]}"; do args+=(--ssh-key "$k"); done
  hcloud server create "${args[@]}"
fi

IP=$(hcloud server ip "$SERVER_NAME")
echo "  IP: $IP"

echo "== Phase 2: hostname =="
ssh -o StrictHostKeyChecking=accept-new "root@$IP" "hostnamectl set-hostname $SERVER_NAME"

echo "== Phase 3: Docker =="
ssh "root@$IP" 'bash -s' <<'REMOTE'
set -euo pipefail
if ! command -v docker >/dev/null; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq ca-certificates curl gnupg nginx certbot python3-certbot-nginx ufw
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  CODENAME=$(. /etc/os-release && echo "$VERSION_CODENAME")
  ARCH=$(dpkg --print-architecture)
  echo "deb [arch=$ARCH signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $CODENAME stable" > /etc/apt/sources.list.d/docker.list
  apt-get update -qq
  apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
fi
docker run --rm hello-world >/dev/null
echo "  ok: docker hello-world erfolgreich"
REMOTE

echo "== Phase 4: nginx + Origin-Cert + UFW =="
ssh "root@$IP" 'bash -s' <<REMOTE
set -euo pipefail
mkdir -p /etc/ssl/werkbank /var/www/werkbank
if [ ! -f /etc/ssl/werkbank/origin.crt ]; then
  openssl req -x509 -nodes -days 3650 -newkey rsa:2048 \\
    -keyout /etc/ssl/werkbank/origin.key \\
    -out /etc/ssl/werkbank/origin.crt \\
    -subj "/CN=$FQDN" 2>/dev/null
fi
cat > /etc/nginx/sites-available/$FQDN <<NGINX
server {
    listen 80;
    listen [::]:80;
    server_name $FQDN;
    return 301 https://\\\$host\\\$request_uri;
}
server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name $FQDN;
    ssl_certificate     /etc/ssl/werkbank/origin.crt;
    ssl_certificate_key /etc/ssl/werkbank/origin.key;
    root /var/www/werkbank;
    location / {
        default_type text/plain;
        return 200 "werkbank: workspace platform — provisioned (sapling st-vmvnq)\\n";
    }
}
NGINX
ln -sf /etc/nginx/sites-available/$FQDN /etc/nginx/sites-enabled/$FQDN
rm -f /etc/nginx/sites-enabled/default
nginx -t && systemctl reload nginx

ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp
ufw allow 80/tcp
ufw allow 443/tcp
ufw --force enable >/dev/null
REMOTE

echo "== Phase 5: Cloudflare DNS (proxied, edge-TLS via universal cert) =="
if ! cf-dns list stayawesome.app 2>/dev/null | grep -q "$FQDN"; then
  cf-dns add-app --proxy "$FQDN" "$IP"
else
  echo "  ok: DNS-Record existiert bereits"
fi

echo "== Phase 6: Acceptance-Checks =="
resolvectl flush-caches 2>/dev/null || true
curl -sSf "https://$FQDN/" >/dev/null && echo "  ok: HTTPS-200 + valides Cloudflare-Edge-Cert"

ssh "root@$IP" '
  for tag in quantbot vault; do
    if dpkg -l 2>/dev/null | grep -iE "^ii.*$tag" \
       || systemctl list-units --all 2>/dev/null | grep -iq $tag \
       || docker ps -a 2>/dev/null | grep -iq $tag; then
      echo "  FAIL: $tag spuren auf werkbank gefunden"; exit 1
    fi
  done
  echo "  ok: QuantBot/Vault nicht installiert"
'

echo "== werkbank provisioniert =="
