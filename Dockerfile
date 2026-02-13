FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY . .
RUN cd cmd/migrate-mariadb && go build -o /src/migrate-mariadb

FROM alpine:3.23.3
WORKDIR /app
COPY --from=builder /src/migrate-mariadb /app/migrate-mariadb

ENTRYPOINT [ "/app/migrate-mariadb" ]
