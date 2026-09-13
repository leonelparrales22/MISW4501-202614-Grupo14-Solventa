#!/usr/bin/env bash
# Despliegue AWS MÍNIMO para el experimento EC-D02: una sola instancia EC2
# corriendo todo el stack (originación, PostgreSQL, Toxiproxy, stub de
# pasarela) vía docker-compose.yml. Sin ALB/NAT/RDS Multi-AZ/ECS — el
# diseño del experimento no exige infraestructura productiva, esto es
# solo para tener evidencia en vídeo de que corre sobre AWS. Mismo
# esqueleto que scripts/deploy-ec2.sh de EC-D01, adaptado a Postgres.
#
# Requiere: AWS CLI configurado (`aws configure`) con un usuario IAM propio
# (no root) con permisos de EC2. NO pegar credenciales en este script ni en
# el chat: se leen del perfil de AWS CLI ya configurado en tu terminal.
#
# Uso:
#   ./scripts/deploy-ec2.sh up        # crea la instancia y despliega el stack
#   ./scripts/deploy-ec2.sh ssh       # abre una sesión SSH a la instancia
#   ./scripts/deploy-ec2.sh down      # TERMINA la instancia (hacerlo apenas
#                                       termines de grabar el vídeo)
set -euo pipefail
cd "$(dirname "$0")/.."

REGION="${AWS_REGION:-us-east-1}"
INSTANCE_TYPE="${INSTANCE_TYPE:-t3.small}"
KEY_NAME="${KEY_NAME:-ec-d02-key}"
TAG="ec-d02-cobro-idempotente-reintentos"
STATE_FILE=".ec2-instance-id"

# AMI Amazon Linux 2023 más reciente (x86_64), resuelta dinámicamente para
# no fijar un ID de AMI que caduque.
resolve_ami() {
  aws ssm get-parameter \
    --name /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64 \
    --region "$REGION" --query 'Parameter.Value' --output text
}

my_ip() {
  curl -s https://checkip.amazonaws.com
}

up() {
  echo "Verificando identidad de AWS..."
  aws sts get-caller-identity --output table

  if [ -f "$STATE_FILE" ]; then
    echo "Ya existe una instancia registrada en $STATE_FILE. Usa 'down' primero si quieres recrearla."
    exit 1
  fi

  if ! aws ec2 describe-key-pairs --key-names "$KEY_NAME" --region "$REGION" >/dev/null 2>&1; then
    echo "Creando key pair $KEY_NAME..."
    aws ec2 create-key-pair --key-name "$KEY_NAME" --region "$REGION" \
      --query 'KeyMaterial' --output text > "${KEY_NAME}.pem"
    chmod 400 "${KEY_NAME}.pem"
  fi

  IP="$(my_ip)"
  echo "Tu IP pública: $IP (se restringe el Security Group a ella)"

  SG_ID=$(aws ec2 create-security-group --group-name "${TAG}-sg" \
    --description "EC-D02: acceso SSH restringido, sin exposición pública del servicio" \
    --region "$REGION" --query 'GroupId' --output text)
  aws ec2 authorize-security-group-ingress --group-id "$SG_ID" --region "$REGION" \
    --protocol tcp --port 22 --cidr "${IP}/32" >/dev/null
  # 8280 solo para grabar el vídeo probando desde tu máquina; si prefieres
  # no exponerlo, coméntalo y usa un túnel SSH (-L 8280:localhost:8280).
  aws ec2 authorize-security-group-ingress --group-id "$SG_ID" --region "$REGION" \
    --protocol tcp --port 8280 --cidr "${IP}/32" >/dev/null

  AMI_ID=$(resolve_ami)
  echo "AMI: $AMI_ID"

  USER_DATA=$(cat <<'EOF'
#!/bin/bash
dnf install -y docker git
systemctl enable --now docker
usermod -aG docker ec2-user
curl -SL https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
  -o /usr/local/bin/docker-compose
chmod +x /usr/local/bin/docker-compose
EOF
)

  INSTANCE_ID=$(aws ec2 run-instances \
    --image-id "$AMI_ID" \
    --instance-type "$INSTANCE_TYPE" \
    --key-name "$KEY_NAME" \
    --security-group-ids "$SG_ID" \
    --user-data "$USER_DATA" \
    --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=${TAG}}]" \
    --region "$REGION" \
    --query 'Instances[0].InstanceId' --output text)

  echo "$INSTANCE_ID" > "$STATE_FILE"
  echo "$SG_ID" > ".ec2-sg-id"
  echo "Instancia $INSTANCE_ID creada. Esperando que esté 'running'..."
  aws ec2 wait instance-running --instance-ids "$INSTANCE_ID" --region "$REGION"

  PUBLIC_IP=$(aws ec2 describe-instances --instance-ids "$INSTANCE_ID" --region "$REGION" \
    --query 'Reservations[0].Instances[0].PublicIpAddress' --output text)
  echo "$PUBLIC_IP" > ".ec2-public-ip"

  echo ""
  echo "Instancia lista: $PUBLIC_IP"
  echo "Esperando ~60s a que cloud-init instale Docker..."
  sleep 60

  echo "Copiando el experimento a la instancia..."
  scp -o StrictHostKeyChecking=no -i "${KEY_NAME}.pem" -r \
    "$(pwd)" "ec2-user@${PUBLIC_IP}:/home/ec2-user/exp-d02"

  echo "Levantando el stack (docker compose up -d)..."
  ssh -o StrictHostKeyChecking=no -i "${KEY_NAME}.pem" "ec2-user@${PUBLIC_IP}" \
    "cd /home/ec2-user/exp-d02 && sudo docker-compose up -d --build"

  echo ""
  echo "==================================================================="
  echo " Listo para grabar. Servicio disponible en: http://${PUBLIC_IP}:8280"
  echo " SSH: ssh -i ${KEY_NAME}.pem ec2-user@${PUBLIC_IP}"
  echo " Dentro de la instancia: cd exp-d02 && ./scripts/toxiproxy.sh down|ambiguo|up"
  echo ""
  echo " IMPORTANTE: cuando termines de grabar, corre:"
  echo "   ./scripts/deploy-ec2.sh down"
  echo " para terminar la instancia y no seguir consumiendo tus créditos."
  echo "==================================================================="
}

ssh_in() {
  PUBLIC_IP=$(cat .ec2-public-ip)
  ssh -i "${KEY_NAME}.pem" "ec2-user@${PUBLIC_IP}"
}

down() {
  if [ ! -f "$STATE_FILE" ]; then
    echo "No hay instancia registrada en $STATE_FILE."
    exit 0
  fi
  INSTANCE_ID=$(cat "$STATE_FILE")
  echo "Terminando instancia $INSTANCE_ID..."
  aws ec2 terminate-instances --instance-ids "$INSTANCE_ID" --region "$REGION" >/dev/null
  aws ec2 wait instance-terminated --instance-ids "$INSTANCE_ID" --region "$REGION"
  echo "Instancia terminada."

  if [ -f ".ec2-sg-id" ]; then
    SG_ID=$(cat .ec2-sg-id)
    echo "Eliminando Security Group $SG_ID..."
    aws ec2 delete-security-group --group-id "$SG_ID" --region "$REGION" || \
      echo "No se pudo borrar aún el SG (a veces tarda unos segundos en liberarse); reintenta en un momento."
    rm -f .ec2-sg-id
  fi
  rm -f "$STATE_FILE" .ec2-public-ip
  echo "Confirma en la consola de AWS (EC2 > Instancias) que no quedó nada corriendo."
}

case "${1:-}" in
  up) up ;;
  ssh) ssh_in ;;
  down) down ;;
  *) echo "Uso: $0 {up|ssh|down}" >&2; exit 1 ;;
esac
