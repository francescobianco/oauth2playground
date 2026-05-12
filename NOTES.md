# Note

IMPORTANTE: toglie dalle variabili di ambiente il prefisso OAUTH2PLAYGROUND_
IMPORTANTE: abbiamo deciso che il redirect in locale non lo fara piu il tocker viene catturare dentro l'installziaone del progetto e poi conseganto localmetne attraveso la connessio curl rimasta appesa
IMPORTANTE: le varibili CLEINT_ID e CLIENT_SECRET sono nel .env e non sono fontire dai nostri utenti ma sono gia presenti nel progetto e non devono essere modificate dagli utenti
IMPORTANTE: gli scopes delle chiamate saranno legati a profli di servzio a cui chiedere i token
IMPORTANTE: l'url di autorizzazione che viene inviato all'utente per il process lo devi mascherare con un url temporaneo con un hash random tipo [HOST]/auth/jdo3j383ur238jdj3df8u238dfu che chiaramente essite sono nel corso della chiamato fatta dall'utente 
IMPORTATNT: gli url suggeriti nella index devono avere HTTPS nel caso lo siano

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
- gmail
- gdrive
- microsoft

## come funziona
il servizio deve comportarsi in questo modo:

se chiamo con curl la rotta /api/google/token risposne con un header http che conrtieneso loa chiamve "open-on-browser" che avara l'infirizo di inizio del
    processo di autenticazione con google quello oauth2 classic il sistema rimane in attesa e non chiude la connessione curl finche non sara completato il process che ricava il token a quel punto si manda al chiamate curl completanto lachiama il payload con il token

## Come funzionano gli scope

in pratica noi abbiamo degli scope di defailt legati al servizio per cui l'itente sta chedendo il token 
ad esempio se l'utente chiama /api/gmail/token allora gli scope saranno quelli legati a gmail se chiama /api/google/token 
per aggiungere degli scope extra basta aggiungerli alla chiamata curl in questo modo

```
curl -i [host]/api/google/token?scopes=scope1,scope2,scope
```
