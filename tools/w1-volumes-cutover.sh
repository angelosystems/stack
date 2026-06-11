#!/bin/bash
# W1-Rest-Cutover (PRD quant-stayawesome-entkopplung):
# /var/lib/docker/volumes weg vom TSDB-Volume /dev/sdb — nur die TSDB-Daten
# bleiben dort (gezielter Einzel-Bind statt globalem Verzeichnis-Bind).
#
# NICHT unbeaufsichtigt ausführen. Voraussetzungen:
#   - RawTick-Chunk-Pipeline abgeschlossen (kein compress/move aktiv)
#   - Mario-Go für das Wartungsfenster (stoppt docker komplett, Minuten-Bereich)
#
# Rollback: fstab-Zeile 13 (globaler Bind) wiederherstellen, docker neu starten.
set -euo pipefail

TSDB_VOL=4f3a0299d30210520a013aa5af6300990be6699598fe21df59a3c9cf5db2a105
SRC=/mnt/hc-tsdb/docker-volumes
STAGE=/var/lib/docker/.volumes-staging

echo "== Preflight =="
docker exec quantbot-tsdb psql -U quantbot -d quantbot -Atc \
  "SELECT count(*) FROM pg_stat_activity WHERE query ILIKE '%compress_chunk%' OR query ILIKE '%SET TABLESPACE%';" \
  | grep -q '^0$' || { echo "Pipeline noch aktiv — Cease."; exit 1; }
NEED=$(du -s --exclude="$TSDB_VOL" "$SRC" | awk '{print int($1/1024/1024)+2}')
AVAIL=$(df --output=avail -BG / | tail -1 | tr -d ' G')
[ "$AVAIL" -gt "$NEED" ] || { echo "Root-Disk zu knapp: brauche ${NEED}G, frei ${AVAIL}G"; exit 1; }
echo "ok: ${NEED}G Bedarf, ${AVAIL}G frei"

echo "== Phase 1: Vorab-rsync bei laufendem Betrieb (verkürzt das Fenster) =="
mkdir -p "$STAGE"
rsync -a --delete --exclude "$TSDB_VOL" "$SRC/" "$STAGE/"

echo "== Phase 2: Wartungsfenster =="
systemctl stop quantbot-tsdb
systemctl stop docker.socket docker

echo "  Delta-rsync (Quelle jetzt eingefroren)"
rsync -a --delete --exclude "$TSDB_VOL" "$SRC/" "$STAGE/"

echo "  Globalen Bind lösen"
umount /var/lib/docker/volumes

echo "  Staging einsetzen (echtes Verzeichnis auf Root-Disk)"
# Reste im verdeckten Original-Verzeichnis konservieren statt löschen
[ -n "$(ls -A /var/lib/docker/volumes 2>/dev/null)" ] && \
  mv /var/lib/docker/volumes /var/lib/docker/volumes.pre-cutover-$(date +%Y%m%d)
mv "$STAGE" /var/lib/docker/volumes

echo "  Gezielter Bind nur für die TSDB-Daten"
mkdir -p "/var/lib/docker/volumes/$TSDB_VOL"
sed -i "s|^/mnt/hc-tsdb/docker-volumes /var/lib/docker/volumes none bind,nofail 0 0$|$SRC/$TSDB_VOL /var/lib/docker/volumes/$TSDB_VOL none bind,nofail 0 0|" /etc/fstab
mount "/var/lib/docker/volumes/$TSDB_VOL"

echo "== Phase 3: Hochfahren + Verifikation =="
systemctl start docker
systemctl start quantbot-tsdb
sleep 15
docker ps --format '{{.Names}}\t{{.Status}}'
findmnt /var/lib/docker/volumes && echo "WARNUNG: globaler Bind existiert noch?!" || echo "ok: volumes auf Root-Disk"
findmnt "/var/lib/docker/volumes/$TSDB_VOL" >/dev/null && echo "ok: TSDB-Bind aktiv"
echo "FERTIG — Alt-Daten auf sdb ($SRC, ohne $TSDB_VOL) nach Soak-Phase aufräumen."
