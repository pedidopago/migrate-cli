FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY . .
RUN go build -o /src/bin/migrate-mariadb ./cmd/migrate-mariadb \
 && go build -o /src/bin/migrate-postgres ./cmd/migrate-postgres

FROM alpine:3.23.3
WORKDIR /app
COPY --from=builder /src/bin/ /app/

# Defaults to MariaDB so existing manifests keep working; a Postgres init
# container overrides it with command: [/app/migrate-postgres, ...].
ENTRYPOINT [ "/app/migrate-mariadb" ]
