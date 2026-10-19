# k6 image of the HA-L01 staging window: the scripts plus the measurement
# sequence. Build context: this folder (tests/load-k6/ha-l01).
FROM grafana/k6:0.54.0
COPY k6/ /scripts/
ENTRYPOINT ["sh", "/scripts/sequence.sh"]
