# Plano #10 — o endpoint S3 precisa do `https://`, e o formulário ensina a omitir

Issue: [#10](https://github.com/tuanmedeiros/pgbackweb-eduardolat/issues/10) ·
Branch: `fix/s3-endpoint-scheme` · Base: `8d8fa58` (`main` com o #18)

Este arquivo é o plano vivo da correção: a tabela de tarefas e o histórico são
atualizados conforme o trabalho avança.

## Contexto

No formulário de destino S3 (criar e editar), o placeholder do campo **Endpoint** é
`s3-us-west-1.amazonaws.com`, sem esquema. Quem segue o exemplo cria um destino que
nunca funciona, e o erro não fala do esquema que falta.

Reproduzido de novo em 18/10/2026 contra um `httptest` local, chamando `S3Test`:

| Endpoint digitado                | Resultado                                                                                          |
| -------------------------------- | -------------------------------------------------------------------------------------------------- |
| `http://127.0.0.1:<porta>`       | conecta                                                                                            |
| `127.0.0.1:<porta>`              | `failed to parse endpoint URL: ... first path segment in URL cannot contain colon`                 |
| `s3.us-east-005.backblazeb2.com` | 3 tentativas e depois `Head "/s3.us-east-005.backblazeb2.com/bkt": unsupported protocol scheme ""` |

- **Onde:** placeholder em
  `internal/view/web/dashboard/destinations/create_destination.go:91` e
  `edit_destination.go:104`; a validação é só `required`, no `createDestinationDTO`
  (`create_destination.go:21`). O mesmo DTO serve criar, editar e testar.
- **Por que quebra:** `internal/integration/storage/s3.go` passa o texto cru como
  `aws.Endpoint{URL: endpoint}`. O SDK não completa o esquema (análise na #10).
- **Origem:** código do upstream, igual no `upstream/main` (`083b70e`). Não foi
  introduzido pelo fork.
- **Relatos no upstream:** [#97](https://github.com/eduardolat/pgbackweb/issues/97)
  (Backblaze, o mesmo erro do print), [#98](https://github.com/eduardolat/pgbackweb/issues/98)
  e [#117](https://github.com/eduardolat/pgbackweb/issues/117) (Cloudflare R2). Nas
  três o mantenedor respondeu "adicione `https://`", e o placeholder ficou igual.
- **Caso pior (medido na #10):** o link de download é pré-assinado sem requisição,
  então com endpoint sem esquema ele volta como um caminho relativo inútil, sem erro.

## Objetivo e critérios de aceite

1. O placeholder mostra a forma que funciona: `https://s3.us-west-1.amazonaws.com`.
2. O campo tem um texto de ajuda dizendo que o esquema é obrigatório e que `http://`
   é aceito para MinIO/Ceph sem TLS.
3. Um endpoint sem `http://` ou `https://` é recusado ao criar, editar ou testar,
   com uma mensagem que diz o que falta, em vez do erro do SDK depois de 3 tentativas.
4. Endpoints que já têm esquema continuam funcionando sem mudança.

## Decisão: recusar no formulário

Escolhida em 18/10/2026: **recusar no formulário** (opção B), com o placeholder e a
ajuda corrigidos (C). A #10 recomendava completar o `https://` dentro do
`createS3Client` (opção A); a comparação abaixo fica como registro.

|                                | A — completar no cliente S3                                 | B — recusar no formulário                                         |
| ------------------------------ | ----------------------------------------------------------- | ----------------------------------------------------------------- |
| O que o usuário vê             | funciona, mas a lista mostra um endpoint diferente do usado | mensagem clara na hora de salvar                                  |
| Destinos já salvos sem esquema | passam a funcionar sozinhos                                 | continuam quebrados até alguém editar (e a edição exige corrigir) |
| MinIO só HTTP sem esquema      | vira `https://`, falha com outro erro                       | recusado com a dica de usar `http://`                             |
| Arquivo tocado                 | `s3.go`, o centro da divergência do fork                    | só a camada de formulário                                         |

Por que B: um destino salvo sem esquema nunca funcionou, então não há backup antigo
dependendo dele; o valor guardado fica igual ao valor usado; e não mexe em `s3.go`,
que já concentra o diff do fork contra o upstream. A + B juntas também é possível,
se você quiser que os destinos antigos voltem sem edição.

## Tarefas

| #   | Tarefa                                                                             | Status                                  |
| --- | ---------------------------------------------------------------------------------- | --------------------------------------- |
| 1   | Confirmar a abordagem (A, B ou A + B)                                              | feito: B                                |
| 2   | Placeholder `https://s3.us-west-1.amazonaws.com` e `HelpText` nos dois formulários | feito                                   |
| 3   | Validação do esquema (`validate.S3Endpoint`) em criar, editar e testar             | feito                                   |
| 4   | Teste que falha sem a validação e passa com ela                                    | feito: 6 casos falham com só `required` |
| 5   | `/ci-local`                                                                        | feito: lint, test, build                |
| 6   | Conferir na UI (criar, editar, testar)                                             | feito: criar, editar e testar           |
| 7   | Revisão do Codex em loop                                                           | feito: limpo na 3ª rodada               |
| 8   | Atualizar `UPSTREAM.md` com a divergência e a proposta upstream                    | feito                                   |

## Histórico

- 18/10/2026 — erro reproduzido de novo, issues do upstream levantadas, plano escrito.
- 18/10/2026 — opção B escolhida. A validação fica em `internal/validate/s3_endpoint.go`
  (função pura, como as vizinhas) e é chamada nos três handlers que usam o
  `createDestinationDTO`. O campo ganhou `pattern` para o navegador avisar antes do
  envio; o servidor confere de novo porque o `pattern` não protege requisições
  feitas fora do formulário. Destinos já salvos e as execuções não passam por essa
  checagem: um endpoint antigo sem esquema continua falhando como antes até alguém
  editar, e a edição exige corrigir.
- 18/10/2026 — conferido com o app rodando (Postgres descartável, sem S3 real). Sem
  esquema, criar, testar e editar devolvem a mensagem nova. Com `https://`, a
  requisição passa da validação e falha só na conexão. O formulário de edição de um
  destino antigo mostra o placeholder novo e o valor salvo sem esquema, que o
  `pattern` impede de salvar sem corrigir.
- 18/10/2026 — revisão do Codex (`gpt-5.6-sol`, `medium`). 1ª rodada limpa. Nos
  prints apareceu que o `pattern` bloqueava no navegador com o balão genérico "Please
  match the requested format"; o input ganhou `title` com o texto de ajuda, que o
  navegador acrescenta ao balão. 2ª rodada (P2): `https://:9000` passava porque
  `u.Host` era `":9000"`; a checagem passou a ser `u.Hostname()`, com um caso de
  teste que falha na versão antiga. 3ª rodada limpa.
