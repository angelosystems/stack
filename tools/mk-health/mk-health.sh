#!/bin/bash
# Health-Cockpit-Probe für die Zentrale (master.stayawesome.app/health).
# Pro Solution: Status + KPIs (Uptime · Restarts · Memory · Latenz) → "läuft sauber?"
# Read-only bis auf den Ledger-Reconcile am Ende (Release-Pipeline WP3b):
# systemctl show/is-active, ss, docker inspect, curl, test -x.
#
# Quelle: /opt/stack/tools/mk-health/mk-health.sh (git) — deployt nach
# /usr/local/bin/mk-health.sh; Timer: mk-health.timer (60 s).
OUT=/var/www/master/health.json
NOW=$(date +%s)

# Inventory: Firma | Icon | Name | tier | checks(space-sep) | public-url
#   tier:   live = soll laufen (down=rot) · gebaut = existiert, evtl. dormant (down=grau)
#   checks: svc:<unit> | http:<port> | port:<port> | docker:<container> | bin:<pfad>
read -r -d '' INV <<'INV'
Stay Awesome|🏨|Schaltzentrale (Cockpit)|live|svc:zentrale.service http:3344|zentrale.stayawesome.app
Stay Awesome|🏨|Master-Kanban|live|svc:master-kanban-serve.service http:7780|master.stayawesome.app
Stay Awesome|🏨|Inbox Zero|live|docker:inbox-zero-services-worker-1|inbox.stayawesome.app
Stay Awesome|🏨|Documenso (Signaturen)|live|docker:documenso http:3500|sign.stayawesome.app
Stay Awesome|🏨|Authentik IdP|live|docker:authentik-worker|idp.stayawesome.app
Stay Awesome|🏨|oauth2-proxy (SSO)|live|svc:oauth2-proxy.service http:4180|auth.stayawesome.app
Stay Awesome|🏨|Finance-OS|live|http:1111|finance.stayawesome.app
Stay Awesome|🏨|CapTable|live|http:3030|cap.stayawesome.app
Stay Awesome|🏨|CRM|live|http:3000|crm.stayawesome.app
Stay Awesome|🏨|Bürgschaft-Pitch|live|svc:sa-buergschaft-pitch.service http:8771|xn--brgschaft-q9a.stayawesome.app
Stay Awesome|🏨|WhatsApp-Bridge|live|svc:whatsapp-bridge.service port:8765|-
Stay Awesome|🏨|Documenso-Hooks|live|svc:documenso-hooks.service|-
Stay Awesome|🏨|Schaltzentrale-API (SSoT)|gebaut|port:5555|-
QuantBot|🤖|Supervisor (Paper)|live|svc:quantbot-supervisor-paper.service http:9090|-
QuantBot|🤖|QuantBot-PG|live|svc:quantbot-pg.service port:54330|-
QuantBot|🤖|TSDB|live|svc:quantbot-tsdb.service|-
QuantBot|🤖|Feed-Listener|live|svc:quantbot-listener.service|-
QuantBot|🤖|Resolver|live|svc:quantbot-resolver.service|-
QuantBot|🤖|Findings-Watcher|live|svc:quantbot-findings-watcher.service|-
QuantBot|🤖|Mining-Runner|live|svc:quantbot-mining-runner.service|-
QuantBot|🤖|Metrics-Tunnel|live|svc:quantbot-metrics-tunnel.service|-
QuantBot|🤖|Polymarket-Proxy|live|svc:polymarket-proxy.service|-
QuantBot|🤖|weft-db|gebaut|port:54332|-
Solartown|🏗️|Town (tmux)|live|svc:solartown-tmux-solartown-f60b93.service port:8081|solartown.stayawesome.app
Solartown|🏗️|Bead-Reactor|live|svc:bead-created-reactor.service|-
Solartown|🏗️|Plan-Decomposer|live|svc:plan-decomposer.service|-
Solartown|🏗️|Epic-Completion-Reactor|live|svc:epic-completion-reactor.service|-
Solartown|🏗️|Kartograph-Listener|live|svc:kartograph-listener.service|-
Solartown|🏗️|Solartown-Public|live|svc:master-kanban-solartown.service http:8889|solartown.stayawesome.app
Solartown|🏗️|gt-llm-sidecar|gebaut|port:4100|-
Vibe Kanban|🛠️|Vibe-Kanban|live|svc:vibe-kanban.service|-
Vibe Kanban|🛠️|VK-Overseer|live|svc:vk-overseer.service|-
Vibe Kanban|🛠️|VK-Watcher|live|svc:vk-watcher.service|-
GrafitMedia|🎬|Kingdom|live|svc:kingdom.service http:3333|kingdom.grafitmedia.de
GrafitMedia|🎬|Paperclip|live|svc:paperclip.service http:3100|paperclip.grafitmedia.de
GrafitMedia|🎬|Activepieces|live|svc:activepieces.service|-
AngeloOS / Brain|🧠|Angelo-Cockpit|live|svc:angelo-cockpit.service http:3701|-
AngeloOS / Brain|🧠|Mario-Angelo|live|svc:mario-angelo.service|-
AngeloOS / Brain|🧠|Infra-Collector|live|svc:angelo-infra-collector.service|-
AngeloOS / Brain|🧠|Mario-Brain-DB|live|docker:mario-brain-db port:5434|-
AngeloOS / Brain|🧠|GitHub-MCP|live|svc:github-mcp.service http:9100|-
AngeloOS / Brain|🧠|Riffado (Plaud-Bridge)|gebaut|port:3010|-
Stack / Infra|⚙️|Host-Metrics|live|svc:mk-host-metrics.service|-
Stack / Infra|⚙️|Health-Probe|live|svc:mk-health.timer|-
Stack / Infra|⚙️|Process-Dashboard|live|svc:process-dashboard.service|-
Stack / Infra|⚙️|one-api (LLM-Gateway)|live|svc:one-api.service http:4000|-
Stack / Infra|⚙️|Restate|live|svc:restate.service port:8080|-
Stack / Infra|⚙️|nginx|live|svc:nginx.service|-
Tools / CLI|🧰|cf-dns|gebaut|bin:/usr/local/bin/cf-dns|-
Tools / CLI|🧰|solartown-rig|gebaut|bin:/usr/local/bin/solartown-rig|-
Tools / CLI|🧰|vk-delegate|gebaut|bin:/usr/local/bin/vk-delegate|-
Tools / CLI|🧰|angelo-reload|gebaut|bin:/usr/local/bin/angelo-reload|-
Tools / CLI|🧰|gt-plan|gebaut|bin:/usr/local/bin/gt-plan|-
INV

# globals fürs KPI-Sammeln (vom ersten svc/http-Check je Solution)
KPI_UP=""; KPI_RE=""; KPI_MEM=""; KPI_MS=""

probe_one() {  # "type:arg" → return 0=ok/1=fail, setzt D + ggf. KPI_*
  local t="${1%%:*}" a="${1#*:}"
  case "$t" in
    svc)
      local st; st=$(systemctl is-active "$a" 2>/dev/null)
      if [ -z "$KPI_UP" ]; then
        local ts re mc ep
        ts=$(systemctl show "$a" -p ActiveEnterTimestamp --value 2>/dev/null)
        ep=$(date -d "$ts" +%s 2>/dev/null); [ -n "$ep" ] && KPI_UP=$((NOW-ep))
        re=$(systemctl show "$a" -p NRestarts --value 2>/dev/null); KPI_RE="$re"
        mc=$(systemctl show "$a" -p MemoryCurrent --value 2>/dev/null)
        [[ "$mc" =~ ^[0-9]+$ ]] && KPI_MEM=$((mc/1048576))
      fi
      [ "$st" = active ] && { D="$a aktiv"; return 0; } || { D="$a $st"; return 1; } ;;
    http)
      local out c ms
      out=$(curl -s -o /dev/null -w '%{http_code} %{time_total}' -m 3 "http://127.0.0.1:$a" 2>/dev/null)
      c=${out%% *}; ms=${out##* }
      [ -z "$KPI_MS" ] && KPI_MS=$(awk "BEGIN{printf \"%d\", $ms*1000}" 2>/dev/null)
      [ "$c" != 000 ] && [ -n "$c" ] && { D=":$a HTTP $c"; return 0; } || { D=":$a keine Antwort"; return 1; } ;;
    port)
      ss -lntH 2>/dev/null | grep -q ":$a " && { D=":$a lauscht"; return 0; } || { D=":$a still"; return 1; } ;;
    docker)
      local s; s=$(docker inspect -f '{{.State.Status}}' "$a" 2>/dev/null)
      [ "$s" = running ] && { D="container $s"; return 0; } || { D="container ${s:-weg}"; return 1; } ;;
    bin)
      [ -x "$a" ] && { D="$(basename $a) vorhanden"; return 0; } || { D="$(basename $a) fehlt"; return 1; } ;;
  esac
  return 1
}

fmt_uptime() { local s=$1; [ -z "$s" ] && { echo ""; return; }
  if [ "$s" -ge 86400 ]; then echo "$((s/86400))d$(( (s%86400)/3600 ))h"
  elif [ "$s" -ge 3600 ]; then echo "$((s/3600))h$(( (s%3600)/60 ))m"
  else echo "$((s/60))m"; fi; }

esc() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'; }

tmp=$(mktemp)
echo "{\"ts\":\"$(date -u +%FT%TZ)\",\"firmen\":{" > "$tmp"
prev=""; ff=1
while IFS='|' read -r firma icon name tier checks url; do
  [ -z "$firma" ] && continue
  if [ "$firma" != "$prev" ]; then
    [ "$ff" = 0 ] && echo "]}," >> "$tmp"; ff=0
    printf '"%s":{"icon":"%s","solutions":[' "$(esc "$firma")" "$icon" >> "$tmp"
    prev="$firma"; fs=1
  fi
  KPI_UP=""; KPI_RE=""; KPI_MEM=""; KPI_MS=""
  ok=0; tot=0; details=""
  for chk in $checks; do
    tot=$((tot+1)); D=""
    probe_one "$chk" && ok=$((ok+1))
    details="${details}${details:+ · }${D}"
  done
  if [ "$ok" = "$tot" ]; then status=up; elif [ "$ok" = 0 ]; then status=down; else status=degraded; fi
  # KPI-JSON bauen (nur gesetzte Felder)
  kpi=""
  [ -n "$KPI_UP" ]  && kpi="${kpi}${kpi:+,}\"uptime\":\"$(fmt_uptime $KPI_UP)\""
  [ -n "$KPI_RE" ]  && kpi="${kpi}${kpi:+,}\"restarts\":$KPI_RE"
  [ -n "$KPI_MEM" ] && kpi="${kpi}${kpi:+,}\"mem_mb\":$KPI_MEM"
  [ -n "$KPI_MS" ]  && kpi="${kpi}${kpi:+,}\"http_ms\":$KPI_MS"
  [ "$fs" = 0 ] && printf ',' >> "$tmp"; fs=0
  printf '{"name":"%s","tier":"%s","status":"%s","detail":"%s","url":"%s","kpi":{%s}}' \
    "$(esc "$name")" "$tier" "$status" "$(esc "$details")" "$url" "$kpi" >> "$tmp"
done <<< "$INV"
echo "]}" >> "$tmp"; echo "}}" >> "$tmp"

if python3 -c "import json; json.load(open('$tmp'))" 2>/dev/null; then
  mv "$tmp" "$OUT"
else
  rm -f "$tmp"; echo "health-json invalid, skip" >&2; exit 1
fi

# Release-Ledger versöhnen (Release-Pipeline WP3b): dieselbe 60-s-Probe, die
# health.json schreibt, bestätigt/errored die Ledger-Head-Zeilen (:5434) und
# denormalisiert die Board-Felder. Nie fatal für die Health-Probe selbst.
timeout 30 /opt/stack/bin/master-kanban deployments reconcile --quiet \
  || echo "deployments reconcile fehlgeschlagen (Ledger :5434 prüfen)" >&2
