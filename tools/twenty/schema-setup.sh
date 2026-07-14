#!/usr/bin/env bash
# Twenty-CRM Schema-Setup (PRD crm-twenty-produktion W3/D7)
#
# Legt die drei Domänen-Bausteine idempotent an (create-if-missing, ändern
# statt löschen — R7: nach Live-Daten-Import KEINE destruktiven Operationen):
#   1. Custom Object "Property"  (Immobilien)
#   2. Custom Object "Investor"  (Fundraising-Stammdaten)
#   3. Opportunity: Select "pipeline" (fundraising | hotel_b2b) + Stage-Optionen
#
# Aufruf:  TWENTY_API_KEY=... ./schema-setup.sh [BASE_URL]
#          BASE_URL Default: https://crm.stayawesome.app
set -euo pipefail

BASE="${1:-https://crm.stayawesome.app}"
KEY="${TWENTY_API_KEY:?TWENTY_API_KEY fehlt (Vault: stayawesome/crm-admin.json)}"
API="$BASE/rest/metadata"
AUTH=(-H "Authorization: Bearer $KEY" -H "Content-Type: application/json")

get_object_id() { # nameSingular -> id oder leer
  curl -fsS "${AUTH[@]}" "$API/objects?limit=200" \
    | jq -r --arg n "$1" '.data[]? | select(.nameSingular==$n) | .id' | head -1
}

ensure_object() { # nameSingular namePlural labelSingular labelPlural icon
  local id; id=$(get_object_id "$1")
  if [ -n "$id" ]; then echo "object $1: existiert ($id)"; echo "$id"; return; fi
  id=$(curl -fsS "${AUTH[@]}" -X POST "$API/objects" -d "$(jq -n \
    --arg ns "$1" --arg np "$2" --arg ls "$3" --arg lp "$4" --arg ic "$5" \
    '{nameSingular:$ns, namePlural:$np, labelSingular:$ls, labelPlural:$lp, icon:$ic}')" \
    | jq -r '.data.id // .data.createOneObject.id // .id')
  echo "object $1: angelegt ($id)" >&2; echo "$id"
}

ensure_field() { # objectId name label type extraJson(optional)
  local oid="$1" name="$2" label="$3" type="$4" extra="${5-}"
  [ -n "$extra" ] || extra='{}'   # NICHT ${5:-{}} — Bash schließt die Expansion am ersten }
  local exists; exists=$(curl -fsS "${AUTH[@]}" "$API/fields?limit=500" \
    | jq -r --arg o "$oid" --arg n "$name" \
      '.data[]? | select(.objectMetadataId==$o and .name==$n) | .id' | head -1)
  if [ -n "$exists" ]; then echo "  field $name: existiert"; return; fi
  curl -fsS "${AUTH[@]}" -X POST "$API/fields" -d "$(jq -n \
    --arg o "$oid" --arg n "$name" --arg l "$label" --arg t "$type" --argjson x "$extra" \
    '{objectMetadataId:$o, name:$n, label:$l, type:$t} + $x')" >/dev/null
  echo "  field $name: angelegt"
}

# ── 1. Property (Immobilien) ────────────────────────────────────────────────
PID=$(ensure_object property properties "Property" "Properties" "IconBuilding" | tail -1)
ensure_field "$PID" adresse       "Adresse"        TEXT
ensure_field "$PID" objektStatus  "Objekt-Status"  SELECT '{"options":[
  {"value":"AKQUISE","label":"Akquise","color":"blue","position":0},
  {"value":"PRUEFUNG","label":"Prüfung","color":"yellow","position":1},
  {"value":"BESTAND","label":"Bestand","color":"green","position":2},
  {"value":"VERKAUFT","label":"Verkauft","color":"gray","position":3}]}'
ensure_field "$PID" kaufpreis     "Kaufpreis"      CURRENCY
ensure_field "$PID" flaecheQm     "Fläche (qm)"    NUMBER
ensure_field "$PID" notizen       "Notizen"        TEXT

# ── 2. Investor (Fundraising) ───────────────────────────────────────────────
IID=$(ensure_object investor investors "Investor" "Investors" "IconCoin" | tail -1)
ensure_field "$IID" investorTyp   "Typ"            SELECT '{"options":[
  {"value":"ANGEL","label":"Business Angel","color":"blue","position":0},
  {"value":"VC","label":"VC","color":"purple","position":1},
  {"value":"BANK","label":"Bank","color":"green","position":2},
  {"value":"FAMILY_OFFICE","label":"Family Office","color":"orange","position":3},
  {"value":"CROWD","label":"Crowd","color":"gray","position":4}]}'
ensure_field "$IID" ticketBetrag  "Ticket-Betrag"  CURRENCY
ensure_field "$IID" quelle        "Quelle"         TEXT
ensure_field "$IID" nextStep      "Next Step"      TEXT

# ── 3. Opportunity: pipeline-Select + Stage-Optionen ───────────────────────
OID=$(get_object_id opportunity)
[ -n "$OID" ] || { echo "FEHLER: Standard-Objekt opportunity nicht gefunden"; exit 1; }
ensure_field "$OID" pipeline "Pipeline" SELECT '{"options":[
  {"value":"FUNDRAISING","label":"Fundraising","color":"purple","position":0},
  {"value":"HOTEL_B2B","label":"Hotel-B2B","color":"blue","position":1}]}'

# Stage-Optionen mergen (nie löschen — R7): Union aus Bestand + beiden Sets
SFID=$(curl -fsS "${AUTH[@]}" "$API/fields?limit=500" | jq -r --arg o "$OID" \
  '.data[]? | select(.objectMetadataId==$o and .name=="stage") | .id' | head -1)
CUR=$(curl -fsS "${AUTH[@]}" "$API/fields?limit=500" | jq -c --arg o "$OID" \
  '[.data[]? | select(.objectMetadataId==$o and .name=="stage")][0].options // []')
WANT='[{"value":"LEAD","label":"Lead"},{"value":"ERSTKONTAKT","label":"Erstkontakt"},
{"value":"PITCH","label":"Pitch"},{"value":"DD","label":"Due Diligence"},
{"value":"TERM_SHEET","label":"Term Sheet"},{"value":"ANGEBOT","label":"Angebot"},
{"value":"VERHANDLUNG","label":"Verhandlung"},{"value":"WON","label":"Won"},
{"value":"LOST","label":"Lost"},{"value":"CLOSED","label":"Closed"}]'
MERGED=$(jq -c -n --argjson cur "$CUR" --argjson want "$WANT" '
  ($cur | map(.value)) as $have
  | $cur + [ $want[] | select(.value as $v | $have | index($v) | not)
             | . + {color:"gray"} ]
  | to_entries | map(.value + {position:.key})')
curl -fsS "${AUTH[@]}" -X PATCH "$API/fields/$SFID" \
  -d "$(jq -c -n --argjson o "$MERGED" '{options:$o}')" >/dev/null \
  && echo "opportunity.stage: Optionen gemergt ($(echo "$MERGED" | jq length) gesamt)"

echo "Schema-Setup fertig."
