

il servizio deve comportarsi in questo modo:

se chiamo con curl la rotta /api/google/token risposne con un header http che conrtieneso loa chiamve "open-on-browser" che avara l'infirizo di inizio del
    processo di autenticazione con google quello oauth2 classic il sistema rimane in attesa e non chiude la connessione curl finche non sara completato il process che ricava il token a quel punto si manda al chiamate curl completanto lachiama il payload con il token



