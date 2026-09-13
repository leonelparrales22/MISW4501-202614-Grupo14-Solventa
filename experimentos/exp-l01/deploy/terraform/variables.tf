variable "region" {
  description = "Región de AWS"
  type        = string
  default     = "us-east-2"
}

variable "imagen_tag" {
  description = "Tag de la imagen en ECR (la pone el script de publicación)"
  type        = string
  default     = "app"
}

# --- Parámetros del experimento. Cambiarlos y hacer apply reconfigura los
#     servicios sin tocar el resto de la infra. ---

variable "n_fuentes" {
  description = "Cuántas fuentes consulta el orquestador en paralelo"
  type        = number
  default     = 3
}

variable "corte_dep_ms" {
  description = "Límite por fuente (enunciado: 120)"
  type        = number
  default     = 120
}

variable "deadline_ms" {
  description = "Tiempo máximo total para todas las fuentes (enunciado: 700)"
  type        = number
  default     = 700
}

variable "lat_p50_ms" {
  description = "Mediana de latencia de las fuentes simuladas (normal: 60, degradado: 150)"
  type        = number
  default     = 60
}

variable "lat_p95_ms" {
  description = "p95 de latencia de las fuentes simuladas (normal: 200, degradado: 600)"
  type        = number
  default     = 200
}

variable "max_conex_host" {
  description = "Tamaño del pool de conexiones del orquestador hacia las fuentes"
  type        = number
  default     = 200
}

# --- Tallas de Fargate ---

variable "orq_cpu" {
  description = "vCPU del orquestador en unidades de Fargate (512 = 0,5 vCPU)"
  type        = number
  default     = 512
}

variable "orq_memoria" {
  description = "Memoria del orquestador en MiB"
  type        = number
  default     = 1024
}
