# Plano — trazer a correção de segurança da upstream v0.5.2

Branch: `fix/upstream-0.5.2-first-user-guard` · Base: `3a9b701` (`main` com o #22) ·
Versão da imagem: `0.5.2-0.4.0`

## Contexto

Na v0.5.1, `POST /auth/create-first-user` não verifica se já existe usuário. Qualquer
pessoa que alcance a instância cria uma conta e passa a ver as connection strings e
as chaves S3. A upstream corrigiu no `0ad8744` (PR #180, merge `083b70e`), lançado
na v0.5.2. Todas as imagens do fork até `0.5.1-0.3.0` têm a falha.

Do que a v0.5.2 traz, o fork só não tinha `0ad8744` e `083b70e`. O resto, incluindo
`5930ed1`, já estava no `main`.

## Decisões

1. **Merge da tag `v0.5.2`, não cherry-pick.** Assim o git sabe que a v0.5.2 está
   incorporada, e um merge futuro da upstream não traz esses commits de novo. O
   merge não teve conflitos.
2. **Versão `0.5.2-0.4.0`, não `0.5.1-0.4.0`.** O esquema é
   `<versão-upstream>-<versão-fork>`, e com o merge a base passa a ser a v0.5.2
   inteira. `0.5.1-…` diria que a imagem não tem a correção. O número do fork segue
   contando.
3. **A migration da upstream roda com outro nome.** `20260828054515` é mais antiga
   que a `20260925000001` do fork, e o goose recusa uma migration não aplicada mais
   antiga que a última aplicada. Reproduzido: um banco na `0.5.1-0.3.0` falha com
   `found 1 missing migrations before current version 20260925000001`. O mesmo
   conteúdo roda como `20261018000001`, com `CREATE UNIQUE INDEX IF NOT EXISTS`.
4. **O teste do handler usa Postgres real**, como os outros testes de banco do repo,
   e só roda com `PBW_TEST_POSTGRES_CONN_STRING`.

## Tarefas

| #   | Tarefa                                                            | Status                                      |
| --- | ----------------------------------------------------------------- | ------------------------------------------- |
| 1   | Merge da `v0.5.2`                                                 | feito, sem conflitos                        |
| 2   | Reproduzir o "missing migrations"                                 | feito: (b) e (d) falham com o nome original |
| 3   | Migration renomeada e idempotente                                 | feito                                       |
| 4   | Cenários (a)–(d) com goose e PostgreSQL 16                        | feito, tabela abaixo                        |
| 5   | Teste do handler (1 usuário recusa, 0 usuários cria)              | feito: falha com o handler da v0.5.1        |
| 6   | `UPSTREAM.md` e `README.md` com `0.5.2-0.4.0` e nota de segurança | feito                                       |
| 7   | `/ci-local`                                                       | feito: lint, test, build                    |
| 8   | PR e revisão do Codex (limpa na 2ª rodada)                        | aberto                                      |
| 9   | Publicar imagem, tag e release                                    | aguardando o dono                           |

## Cenários de migration

`goose up` com os arquivos desta branch, PostgreSQL 16, em 18/10/2026:

| Banco antes                        | Nome original da upstream     | Com `20261018000001`                        |
| ---------------------------------- | ----------------------------- | ------------------------------------------- |
| (a) novo                           | ok, índice criado             | ok, índice criado                           |
| (b) fork `0.5.1-0.3.0`, 1 usuário  | **falha**: missing migrations | ok, índice criado                           |
| (c) upstream `v0.5.2`, 1 usuário   | ok, índice mantido            | ok, índice mantido                          |
| (d) fork `0.5.1-0.3.0`, 2 usuários | **falha**: missing migrations | falha com o alerta de segurança, sem índice |

Os bancos de partida foram montados com `goose up` usando as migrations das tags
`v0.5.1-0.3.0` e `v0.5.2`, e os usuários inseridos direto na tabela.

## Revisão do Codex (`gpt-5.6-sol`, `medium`)

1. **1ª rodada, P2, procede.** Num banco vindo da upstream v0.5.2, o `down` da
   `20261018000001` apagava o índice que a `20260828054515` criou e que segue
   registrada como aplicada. O `down` agora só apaga o índice quando a
   `20260828054515` não está aplicada. Medido com PostgreSQL 16: com o `down` antigo,
   o banco vindo da v0.5.2 ficava sem índice; com o novo, mantém. Vindo do fork, o
   índice ainda é apagado. Os cenários (a)–(d) não mudaram.
2. **2ª rodada:** limpa.
