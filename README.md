# Migrate CLI

```sh
go install github.com/pedidopago/migrate-cli/cmd/migrate-mariadb@master
```

## Parâmetros

| flag | env | default | o que faz |
|---|---|---|---|
| `--database-url` | `DATABASE_URL` | — | DSN |
| `--migrations` | `MIGRATION_URL` | — | diretório (`file://...`), obrigatório |
| `--command` | `MIGRATION_COMMAND` | `sync` | `up`, `down`, `sync`, `force N`, `steps N`, `new`, `check`, ou um número |
| `--lock-wait-timeout` | `MIGRATION_LOCK_WAIT_TIMEOUT` | `10` | segundos de espera por metadata lock; `0` mantém o default do servidor |
| `--allow-down` | `MIGRATION_ALLOW_DOWN` | `false` | permite migração para trás |

### `--lock-wait-timeout`

Servidores de produção costumam ter `lock_wait_timeout=86400` — um dia inteiro. Mesmo um `ALTER ... ALGORITHM=INSTANT` precisa de um metadata lock exclusivo por um instante, e pedidos de MDL são FIFO: se a `ALTER` fica na fila, **toda query naquela tabela fila atrás dela**. Falhar em segundos e repetir custa uma execução; esperar um dia custa a tabela.

O valor vai no DSN, não num `SET` após conectar, porque o driver de migração usa pool e uma variável de sessão numa conexão não alcança as outras. Um `lock_wait_timeout` já presente na URL tem precedência.

### `--allow-down`

Migração para trás é recusada por padrão, com saída **11**. O default assume produção, que é onde isto roda sem supervisão como init container — e lá um rollback derruba colunas e tabelas de um banco vivo, com cada arquivo rodando fora de transação.

A guarda cobre **os quatro caminhos que descem**, não só o comando `down`:

- `--command=down` — desfaz todas
- `--command=steps -N` — contagem negativa
- `--command=sync` — quando o banco está **à frente** dos arquivos
- `--command=N` — quando `N` é menor que a versão atual

Os dois últimos são os que abrem por acidente: acontecem sozinhos quando uma imagem antiga sobe sobre um schema novo.

## Push

```sh
aws ecr-public get-login-password --region us-east-1 | docker login --username AWS --password-stdin public.ecr.aws

docker build -t migrate-cli-maria .

docker docker tag migrate-cli-maria:latest public.ecr.aws/n9d8f3f1/pedidopago-public/migrate-cli:latest

docker push public.ecr.aws/n9d8f3f1/pedidopago-public/migrate-cli:latest
```
