variable "region" {
  description = "Región de AWS. Debe soportar VPC Link V2 para APIs REST."
  type        = string
  default     = "us-east-1"
}

variable "prefijo" {
  description = "Prefijo de los nombres de recursos."
  type        = string
  default     = "expd03"
}

variable "operador_cidr" {
  description = "CIDR desde el que se inyectan las fallas (IP pública del operador). Vacío = detectar automáticamente."
  type        = string
  default     = ""
}

# --- Monitor de salud del ALB (variables de la matriz) ---

variable "hc_intervalo" {
  description = "Segundos entre health checks del target group (5-300)."
  type        = number
  default     = 10
}

variable "hc_timeout" {
  description = "Segundos de espera de cada health check (2-120, menor que el intervalo)."
  type        = number
  default     = 5
}

variable "hc_umbral_unhealthy" {
  description = "Chequeos fallidos consecutivos para declarar el target unhealthy (2-10)."
  type        = number
  default     = 2
}

variable "hc_umbral_healthy" {
  description = "Chequeos exitosos consecutivos para declarar el target healthy (2-10)."
  type        = number
  default     = 2
}

variable "deregistration_delay" {
  description = "Segundos de draining al retirar un target."
  type        = number
  default     = 30
}

variable "alb_idle_timeout" {
  description = "Segundos que el ALB espera una respuesta antes de responder 504."
  type        = number
  default     = 10
}

variable "alb_interno" {
  description = "true = ALB interno alcanzable solo por el VPC Link; false = ALB público (alternativa de contingencia)."
  type        = bool
  default     = true
}

# --- Pool de conexiones y health del stub (variables de la matriz) ---

variable "pool_max_lifetime" {
  description = "Vida máxima de una conexión del pool (duración de Go; 0 = ilimitada)."
  type        = string
  default     = "0"
}

variable "pool_max_idle_time" {
  description = "Inactividad máxima de una conexión del pool (duración de Go; 0 = ilimitada)."
  type        = string
  default     = "0"
}

variable "pool_max_open" {
  type    = number
  default = 10
}

variable "pool_max_idle" {
  type    = number
  default = 10
}

variable "health_mode" {
  description = "superficial (proceso vivo) o profundo (además hace ping a la base)."
  type        = string
  default     = "superficial"
}

# --- Cómputo ---

variable "imagen_tag" {
  description = "Etiqueta de la imagen del stub en ECR."
  type        = string
  default     = "latest"
}

variable "tareas_deseadas" {
  type    = number
  default = 2
}

variable "container_health_check" {
  description = "true = ECS ejecuta su propio health check dentro del contenedor (cada 5 s, timeout 3 s, 2 reintentos) además del monitor del ALB."
  type        = bool
  default     = false
}

variable "cpu_trabajo" {
  description = "Costo de CPU por solicitud que emula el stub (duración de Go, p. ej. 10ms). 0 = ninguno. Solo para las ejecuciones de carga alta."
  type        = string
  default     = "0"
}

variable "usage_plan_tasa" {
  description = "Tasa sostenida (req/s) del usage plan del socio de prueba."
  type        = number
  default     = 200
}

variable "usage_plan_rafaga" {
  description = "Ráfaga del usage plan del socio de prueba."
  type        = number
  default     = 400
}

variable "tarea_cpu" {
  type    = number
  default = 256
}

variable "tarea_memoria" {
  type    = number
  default = 512
}

# --- Base de datos ---

variable "db_clase" {
  description = "Clase de la instancia RDS."
  type        = string
  default     = "db.t4g.micro"
}

variable "db_version" {
  description = "Versión mayor de PostgreSQL."
  type        = string
  default     = "16"
}

# --- Control de costo ---

variable "presupuesto_correo" {
  description = "Correo que recibe las alertas del presupuesto. Vacío = no se crea presupuesto."
  type        = string
  default     = ""
}

variable "presupuesto_limite_usd" {
  type    = number
  default = 20
}
