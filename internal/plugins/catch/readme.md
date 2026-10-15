# Tacos Dyno-Proxy Plugin

This is an example Dyno-Proxy Plugin. It will add an `X-TACOS` header to every response.

Dyno Proxy will load the source and configure Traefik middleware on the appropriate paths.

To try this plugin copy it to your project

```
cp ./plugins/tacos <app-root>/plugins/tacos
```

Configure in `<app-root>/project.toml`

```

```