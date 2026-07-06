#!/usr/bin/env bash
# WP0 Beobachtungs-Minimum (Coding Factory, PRD paperclip-coding-fabrik WP0).
# Edge-triggered Host-Frühwarnung: meldet Zustandswechsel, nie Dauerfeuer.
# Befristetes Interim auf werkstatt (Restrisiko-Ablauf = WP5-Live, s. PRD).
# Meldeweg: critical -> WhatsApp (lokale whatsmeow-Bridge :8765) + journald;
# warn -> journald. Board-Anbindung folgt in WP3 (Issue-Sink), bewusst geparkt.
set -u
STATE_DIR=/var/lib/wp0-frueh-warnung
WA_URL=http://127.0.0.1:8765/api/send
WA_JID=4915153509052@s.whatsapp.net
HOST=$(hostname)

MEM_AVAIL_KB=$(awk '/MemAvailable/{print $2}' /proc/meminfo)
MEM_AVAIL_GB=$((MEM_AVAIL_KB/1024/1024))
SWAP_USED_KB=$(awk '/SwapTotal/{t=$2}/SwapFree/{f=$2}END{print t-f}' /proc/meminfo)
PSI_SOME=$(awk -F'avg10=' '/some/{split($2,a," ");print a[1]}' /proc/pressure/memory | cut -d. -f1)

# NRestarts-Sturm: Summe über alle Services, Delta seit letztem Lauf
RESTART_SUM=$(systemctl list-units --type=service --state=running --no-legend --plain 2>/dev/null | awk '{print $1}' | xargs -r -n1 systemctl show -p NRestarts --value 2>/dev/null | awk '{s+=$1}END{print s+0}')
PREV_SUM=$(cat "$STATE_DIR/restart_sum" 2>/dev/null || echo "$RESTART_SUM")
echo "$RESTART_SUM" > "$STATE_DIR/restart_sum"
RESTART_DELTA=$((RESTART_SUM-PREV_SUM))

# Slice-Druck: MemoryCurrent/MemoryHigh je überwachter Slice
slice_pct() {
  local cur high
  cur=$(systemctl show "$1" -p MemoryCurrent --value 2>/dev/null)
  high=$(systemctl show "$1" -p MemoryHigh --value 2>/dev/null)
  [[ "$cur" =~ ^[0-9]+$ && "$high" =~ ^[0-9]+$ && "$high" -gt 0 ]] || { echo 0; return; }
  echo $((cur*100/high))
}
VK_PCT=$(slice_pct vk.slice)
USER_PCT=$(slice_pct user.slice)

verdict() { # name wert warn_ab crit_ab richtung(gt|lt)
  local v=$2 w=$3 c=$4 d=$5
  if [ "$d" = lt ]; then
    [ "$v" -lt "$c" ] && { echo critical; return; }
    [ "$v" -lt "$w" ] && { echo warn; return; }
  else
    [ "$v" -gt "$c" ] && { echo critical; return; }
    [ "$v" -gt "$w" ] && { echo warn; return; }
  fi
  echo ok
}

report() { # check level detail
  local check=$1 level=$2 detail=$3
  local prev_file="$STATE_DIR/state_$check" prev
  prev=$(cat "$prev_file" 2>/dev/null || echo ok)
  [ "$level" = "$prev" ] && return          # edge-triggered: nur Wechsel
  echo "$level" > "$prev_file"
  logger -t wp0-frueh-warnung "[$level] $check: $detail (vorher: $prev)"
  if [ "$level" = critical ] || { [ "$prev" = critical ] && [ "$level" = ok ]; }; then
    local txt="⚠️ WP0 $HOST — $check: $level ($detail)"
    [ "$level" = ok ] && txt="✅ WP0 $HOST — $check: wieder OK ($detail)"
    curl -sf -m 10 -X POST "$WA_URL" -H 'Content-Type: application/json' \
      -d "{\"recipient\":\"$WA_JID\",\"message\":\"$txt\"}" >/dev/null 2>&1 \
      || logger -t wp0-frueh-warnung "WA-Zustellung fehlgeschlagen ($check $level)"
  fi
}

report mem_available "$(verdict mem $MEM_AVAIL_GB 6 4 lt)" "MemAvailable=${MEM_AVAIL_GB}G (warn<6, crit<4)"
report psi_memory    "$(verdict psi ${PSI_SOME:-0} 10 25 gt)" "PSI some avg10=${PSI_SOME:-0} (warn>10, crit>25)"
report swap_used     "$(verdict swp $((SWAP_USED_KB/1024/1024)) 1 4 gt)" "Swap=$((SWAP_USED_KB/1024))M (warn>1G, crit>4G)"
report restart_sturm "$(verdict rst $RESTART_DELTA 10 40 gt)" "NRestarts-Delta=$RESTART_DELTA/Tick (warn>10, crit>40)"
report vk_slice      "$(verdict vks $VK_PCT 80 95 gt)" "vk.slice=${VK_PCT}% von MemoryHigh"
report user_slice    "$(verdict uss $USER_PCT 80 95 gt)" "user.slice=${USER_PCT}% von MemoryHigh"

if [ "${1:-}" = "--selftest" ]; then
  curl -sf -m 10 -X POST "$WA_URL" -H 'Content-Type: application/json' \
    -d "{\"recipient\":\"$WA_JID\",\"message\":\"✅ [WP0-Selbsttest] Frühwarn-Reflex auf $HOST scharf: MemAvailable=${MEM_AVAIL_GB}G, PSI=${PSI_SOME:-0}, vk.slice=${VK_PCT}%, user.slice=${USER_PCT}%. Meldeweg bewiesen (critical→WhatsApp, Tick 60s).\"}" \
    && echo "selftest: WA zugestellt" || echo "selftest: WA FEHLGESCHLAGEN"
fi
