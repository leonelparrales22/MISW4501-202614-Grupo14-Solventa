# Multi-stage: the same image runs locally, in CI and on AWS (ECS Fargate).
# Build context: repository root (uses go.work and pkg/).
FROM golang:1.23 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/audit ./services/audit/cmd/audit

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/audit /audit
EXPOSE 8080
ENTRYPOINT ["/audit"]
