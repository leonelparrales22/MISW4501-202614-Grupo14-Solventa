# Build context: repository root (uses go.work).
FROM golang:1.23 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/kyc ./stubs/kyc/cmd/kyc

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/kyc /kyc
EXPOSE 9020
ENTRYPOINT ["/kyc"]
