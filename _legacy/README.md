
Ce répertoire contient l'implémentation de notre serveur écrit en go.

Pour l'instant, il ne fait pas grand chose ; il ne s'occupe à priori que de l'authentification
venant de OAuth ou SAML. Il a besoin de PostgresT pour pouvoir s'occuper des requêtes SQL.

# TODO

 - [ ] Intégrer une solution comme hachicorp Vault pour sécuriser les tokens / certificats utilisés par le serveur
 - [ ] Intégrer du logging dans un SIEM
 - [ ] Rendre la création des endpoints oauth / SAML dynamique de manière à ce que le serveur puisse se reconfigurer
     même une fois en train de tourner.

Plus expérimental

 - [ ] Déporter l'authentification user / password vers un autre service plus global (vault?)

# Prérequis pour la compilation

Si t'as pas go, tu l'installes avec ça

```
sudo add-apt-repository ppa:longsleep/golang-backports
sudo apt update
sudo apt install golang-go
```

One liner sous ubuntu pour les tools : `apt install musl musl-tools musl-dev make entr`

 - `go` (la 1.15 minimum), car le serveur est écrit en go (https://golang.org/).
 - L'utilitaire `entr`, qui permet d'exécuter une commande lorsqu'un fichier change. Utilisé pour le `make watch`
   du serveur.
 - L'utilitaire `make`, qui sert à codifier de façon élégante les étapes de build du projet. Traditionnellement
   plus utilisé dans le monde C/C++, mais "language-agnostic", il est bien pratique quand même.
 - Les bibliothèques de dev de la `libc-musl`, car l'image docker qu'on construit est basée sur la distribution
   *alpine*, dont la libc est musl et pas la glibc traditionnelle. On l'utilise car elle permet de réduire très
   sensiblement la taille des images docker qu'on crée par la suite (+ de 100 mégas pour une debian docker sans rien
   d'installé, 3 mégas de base pour alpine...)
 - docker, si on veut pouvoir construire l'image

Si on a tout ça, on peut taper

 - `make`, pour juste compiler
 - `make image`, pour compiler et construire l'image localement
 - `make upload`, pour faire tout ça et en plus l'envoyer sur notre registry. Note qu'il faut être authentifié à celui-ci.

# Comment développer avec

On peut faire `make watch`, qui le recompile alors dès qu'un fichier source change. Comme c'est go, ça compile *très* vite.

Dans un docker-compose.yml qui s'en sert, on lui laisse `image: .../goserver:<version>`, mais on peut overrider le volume
(de préférence dans un `docker-override.yml`), en faisant pointer `<racine swapp>/goserver:/server`. L'image docker utilise
entr pour killer le serveur et le relancer si il détecte qu'il change.

# dmut

Dmut version fichiers SQL est appelé par défaut si le serveur trouve le fichier `/server/dmut/index.dmut`.

# Endpoints de connection

Un `GET` sur `/auth/endpoints` renvoie la liste des méthodes d'authentification activées sur le serveur.

# Mot de passes

Pour désactiver le login par mot de passe, mettre autre chose qu'une chaine de caractères vide dans `SW_DISABLE_PASSWD`.

# Impersonation

À des fins de testing, il est possible de donner un identifiant de rôle à `SW_IMPERSONATOR_ROLE`. Si l'utilisateur actuellement connecté possède le rôle en question (et qu'il s'est connecté *après* l'attribution d'une valeur à cette variable,) il a alors le droit de se connecter en n'importe quel utilisateur, sur l'URL `/auth/impersonate/<username>`

Il est déconseillé de mettre cette variable en production.

# Généralités sur l'authentification

* `SW_ACCESSTOKEN_EXPIRY`: le nombre d'heures à partir de laquelle le cookie n'est plus valide et l'utilisateur doit se réauthentifier.
    Si non fourni, alors la session expire dès la fermeture du browser.
    Le délai d'expiration est rafraîchi à chaque nouvelle requête. Il s'agit donc d'une durée à partir de laquelle l'utilisateur
    peut retourner sur le site après avoir fermé son browser pendant laquelle il peut y retourner sans s'authentifier.

    Des clients peuvent ne pas apprécier et demander à ce qu'il n'y ait pas de délai et que l'utilisateur doive se réauthentifier
    dès que son browser s'est fermé.

# OAuth

Tout se fait via variables d'environnement. À terme, on y rajoutera la possibilité d'aller lire des secrets
venant par exemple de vault (possiblement).

Ensuite on balance l'utilisateur sur `https://<URL_SERVEUR>/oauth/<PROVIDER>` où provider est google ou salesfoce.

## Salesforce

`SW_SFDC_KEY=la cle`
`SW_SFDC_SECRET=le secret`

## Google

`SW_GOOGLE_KEY=la cle`
`SW_GOOGLE_SECRET=le secret`

# SAML

Pour le SAML, il faudra vraisemblablement communiquer l'adresse de la metadata SAML à l'IDP (IDentity Provider) qui doit autoriser notre app.
Elle est généralement accessible sur `https://<URL_SERVEUR>/saml/<NOM_IDP_LOCAL>/saml/metadata`, où NOM_IDP_LOCAL correspond au nom qu'on donne
dans la variable.

Ensuite, on peut initier un login utilisateur en l'envoyant sur `https://<URL_SERVEUR>/saml/<NOM_IDP_LOCAL>/saml/login`.

Oui, il y a deux fois `saml` dans l'URL, c'est indépendant de ma volonté.

La variable d'environnement est `SW_SAML_IDP` :

- `SW_SAML_IDP=samltest:https://samltest.id/saml/idp`
- Si il y en a plusieurs : `SW_SAML_IDP=samltest:https://samltest.id/saml/idp; tkd:https://okta.com/saml/takeda/idp`