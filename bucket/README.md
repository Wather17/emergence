# Scoop bucket

`emergence.json` será criado aqui automaticamente pela primeira release estável (`vX.Y.Z`), com URLs e SHA-256 dos pacotes publicados. Não há manifesto de exemplo instalável com hashes fictícios.

Com o repositório público e a primeira release publicada:

```powershell
scoop bucket add emergence https://github.com/Wather17/emergence
scoop install emergence/emergence
```

Para atualizar: `scoop update emergence`. A atualização e a desinstalação do CLI não alteram suas vaults, que ficam fora da pasta de instalação.
