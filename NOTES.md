# Note

IMPORTANTE: abbiamo deciso che il redirect in locale non lo fara piu il tocker viene catturare dentro l'installziaone del progetto e poi conseganto localmetne attraveso la connessio curl rimasta appesa

## Cosa presenta la home 


Sezione: How it works
1. Copy the command below and paste it into your terminal
2. Open the URL in your browser
3. Authorize the application to access your account

The terminal captures the token place it where you want for development purposes, for example in a .env file or in a secret manager
Your access token appears right in the terminal

il comando suggerito sara il seguente

```
curl -i [host]/api/google/token > token.txt
```

lista dei provider supportati:

- google
- microsoft

## come funziona
il servizio deve comportarsi in questo modo:

se chiamo con curl la rotta /api/google/token risposne con un header http che conrtieneso loa chiamve "open-on-browser" che avara l'infirizo di inizio del
    processo di autenticazione con google quello oauth2 classic il sistema rimane in attesa e non chiude la connessione curl finche non sara completato il process che ricava il token a quel punto si manda al chiamate curl completanto lachiama il payload con il token



