# Migrate CLI

```sh
go install github.com/pedidopago/migrate-cli/cmd/migrate-mariadb@master
```

## Push

```sh
aws ecr-public get-login-password --region us-east-1 | docker login --username AWS --password-stdin public.ecr.aws

docker build -t migrate-cli-maria .

docker docker tag migrate-cli-maria:latest public.ecr.aws/n9d8f3f1/pedidopago-public/migrate-cli:latest

docker push public.ecr.aws/n9d8f3f1/pedidopago-public/migrate-cli:latest
```
