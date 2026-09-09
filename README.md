# Emergence

CLI em Go para manter uma pasta de notas criptografada dentro da sua vault local do Obsidian. Funciona no Windows e Linux, sem plugin ou serviço em segundo plano.

## Instalação

Requer Go 1.26 ou superior para compilar. O binário pronto não depende de Go ou do comando `age` instalado.

No Linux/WSL, a partir deste repositório:

```sh
go build -o bin/emergence ./cmd/emergence
go install ./cmd/emergence
```

`go install` coloca o executável em `GOBIN`, ou em `$(go env GOPATH)/bin` quando `GOBIN` não estiver definido. Adicione esse diretório ao seu `PATH`.

Para gerar o executável Windows a partir do WSL:

```sh
GOOS=windows GOARCH=amd64 go build -o bin/emergence.exe ./cmd/emergence
```

Copie `bin/emergence.exe` para uma pasta no `Path` do Windows. No Windows, você também pode compilar diretamente com `go build -o bin/emergence.exe ./cmd/emergence`. Para dispositivos ARM64, troque `GOARCH` por `arm64`.

## Uso

No PowerShell, entre na **raiz da vault**, onde normalmente fica `.obsidian`:

```powershell
cd "C:\Users\voce\Documents\Minha Vault"
emergence init
emergence unlock
```

`init` pede uma senha com confirmação e cria um armazenamento vazio, inicialmente trancado. `unlock` pede essa senha e cria `Morning Pages/`; escreva suas notas nessa pasta pelo Obsidian. Para escolher outro nome, use `emergence init --folder "Diário"` na inicialização.

Depois de salvar suas notas e encerrar a edição:

```powershell
emergence lock
emergence status
```

`lock` pede **a mesma senha**, verifica a nova cópia criptografada e remove os arquivos abertos. `status` informa `aberta`, `trancada` ou `incompleta`; não pede senha nem verifica a integridade criptográfica do arquivo.

Os comandos são iguais no Linux. `unlock`, `lock` e `status` também encontram a configuração quando executados em subpastas da vault. Execute o fechamento de uma pasta que continuará existindo, como a raiz da vault.

Uma pasta privada por vault. A pasta inicial deve ser nova; para adicionar notas existentes, desbloqueie e mova-as manualmente para ela. Arquivos comuns, anexos e subpastas vazias são preservados. Links, arquivos especiais e nomes incompatíveis com Windows são recusados. As permissões e datas originais dos arquivos não são preservadas.

Para o uso rotineiro no Windows, prefira `emergence.exe` no PowerShell. O WSL serve para desenvolvimento; acesso simultâneo pelos dois sistemas ao mesmo armazenamento não é suportado.

## Armazenamento e segurança

```text
Minha Vault/
  .obsidian/
  .emergence/
    config.json       # versão e nome da pasta
    sealed.age        # TAR criptografado: conteúdo, nomes e subpastas
    operation.lock    # trava entre processos; pode permanecer após o uso
  Morning Pages/      # presente apenas enquanto aberta, ou numa falha incompleta
```

A criptografia usa [age](https://pkg.go.dev/filippo.io/age), com senha via scrypt nos parâmetros padrão da biblioteca. O arquivo pode ser descriptografado com ferramentas compatíveis com age; não há algoritmo próprio. A senha é lida no terminal sem eco e não é aceita em argumentos, registrada em logs nem salva em disco. O programa não promete eliminar todas as cópias da senha da memória do processo Go.

Enquanto aberta, a pasta contém **arquivos normais**, acessíveis ao editor e a outros programas com suas permissões. O CLI não protege contra malware ou processos alterando ativamente o armazenamento. Use uma senha longa e exclusiva. Não há recuperação nem troca de senha nesta versão.

No Linux, arquivos e pastas criados usam permissões restritas ao usuário. No Windows, o acesso também depende das ACLs herdadas da pasta da vault; o CLI não altera essas ACLs.

Trancar remove as cópias abertas, mas **não garante apagamento físico**, nem remove cópias do Obsidian, plugins, histórico, lixeira, backups ou indexadores. Configure essas ferramentas conforme sua necessidade. Não use sync/Git para essa pasta sem considerar que podem registrar as notas enquanto abertas. Mesmo trancado, o nome da pasta privada e o tamanho do arquivo criptografado ficam visíveis.

Antes de `lock`, salve e feche a edição das notas; fechar o Obsidian é a opção mais previsível. O CLI verifica alterações e recusa remover arquivos divergentes, mas não consegue impedir que outro programa escreva entre a verificação e a remoção ou recrie a pasta depois. Não edite durante a operação.

Mantenha backups de `.emergence/` **depois de um fechamento concluído**. Enquanto a pasta está aberta, `sealed.age` ainda contém a versão do último fechamento, sem as edições recentes.

## Falhas e recuperação

Durante uma operação, `.emergence/txn/` guarda o registro, temporários e, no fechamento, a versão criptografada anterior. O CLI só remove notas depois de gravar, fechar, reler e autenticar o novo arquivo. Duas instâncias nativas não operam na mesma vault ao mesmo tempo; a trava é liberada pelo sistema quando o processo termina.

Se um comando for interrompido:

1. Encerre a edição e consulte `emergence status`.
2. Repita o **mesmo comando**, com a mesma senha. Uma operação de `lock` incompleta deve ser retomada com `lock`, e uma de `unlock` com `unlock`.
3. Se houver um arquivo em uso ou erro de permissão, resolva a causa e repita. O CLI mantém a transação incompleta enquanto ainda houver conteúdo a remover.

Uma interrupção após a conclusão pode resultar em “já está aberta/trancada” ao repetir o comando; confira `status`. Arquivos são sincronizados antes de prosseguir. A sincronização de diretórios também é feita no Linux; não há garantia equivalente para todos os casos de perda de energia no Windows ou em sistemas de arquivos de rede. Use armazenamento local e backups.

Se o CLI detectar **conteúdo novo ou alterado após preparar a criptografia**, ele preserva o que restou e pede recuperação manual. Não apague `txn` nem sobrescreva as notas. Primeiro copie toda a vault, incluindo `.emergence/` e a pasta aberta, para um local seguro. Essa cópia pode conter texto aberto.

Para recuperação manual, trabalhe exclusivamente nessa cópia:

- `sealed.age`, quando presente, é a versão criptografada publicada.
- `txn/previous.age`, quando presente, contém a versão anterior ao fechamento interrompido.
- `txn/next.age`, quando presente e íntegro, contém a tentativa de novo fechamento. Pode estar incompleto; só use se a descriptografia terminar com sucesso.
- A pasta aberta pode conter edições mais recentes que esses arquivos. Preserve-a e compare os conflitos manualmente.

Com o [CLI age](https://github.com/FiloSottile/age) instalado separadamente, descriptografe cada candidato para um TAR diferente, usando um diretório de recuperação vazio fora da vault:

```sh
age --decrypt -o recovered.tar /caminho/da/copia/.emergence/sealed.age
```

No PowerShell, use caminhos Windows entre aspas. **Só extraia o TAR se age terminar sem erro.** Um TAR parcial de uma descriptografia malsucedida não é um backup verificado. Extraia cada candidato em uma pasta separada com `tar -xf recovered.tar -C pasta-vazia`, compare com as notas abertas preservadas e reúna a versão desejada. Para arquivos de origem desconhecida, inspecione os caminhos antes da extração.

Inicialize uma vault nova em outro diretório, desbloqueie, copie as notas recuperadas e teste um ciclo completo de trancar/desbloquear. Mantenha a cópia original até confirmar o resultado. Essa reconstrução evita descartar versões potencialmente únicas na transação danificada.

Se uma queda interromper o próprio `init` e deixar `.emergence-init/`, preserve essa pasta fora da vault e repita `init`; a inicialização não importa notas existentes.

## Desenvolvimento e validação

```sh
go vet ./...
go test -race ./...
go build -o bin/emergence ./cmd/emergence
```

Os testes cobrem ciclos completos, pasta vazia, anexos, acentos, senha errada, truncamento/adulteração, caminhos maliciosos, links, conflitos, alterações concorrentes e recuperação em etapas de publicação e remoção. Os testes internos usam scrypt reduzido para executar rapidamente; o executável usa o padrão age.

Há testes específicos de permissões no Linux (ignorados quando executados como root) e de arquivos abertos sem compartilhamento de exclusão no Windows. A CI executa testes com detector de corridas e compila em ambos os sistemas, disponibilizando os binários como artefatos. Compilar para Windows no Linux não substitui executar os testes no Windows.

Validação manual de integração antes de usar notas reais:

1. Crie uma vault descartável no Obsidian para Windows e execute `init`/`unlock` no PowerShell.
2. Escreva uma nota com acentos e adicione um anexo; salve e feche o Obsidian.
3. Execute `lock`; confirme `status` trancada e ausência da pasta aberta.
4. Execute `unlock`, reabra o Obsidian e confira nota e anexo.
5. Repita com uma senha errada e confirme que não aparecem notas abertas.

Fora desta versão: sincronização, criação da nota diária, bloqueio automático, múltiplas pastas, troca de senha e integração com chaveiro do sistema.
