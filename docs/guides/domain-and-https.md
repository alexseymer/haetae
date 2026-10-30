# A domain with HTTPS

You can give a deployed project a domain name, and Haetae will serve it over HTTPS. In our example the shop's web frontend should answer at `shop.example.com`.

## What you need first

The domain's DNS record must already point to hel1-prod. Ports 80 and 443 on hel1-prod must be free and reachable from the internet, because the certificate is requested through them. The domain must be a plain lower-case host name, with no wildcard, port, IP address or `https://` in front.

## Set it up

Open the project (or create it) and fill in three fields together: **Domain** `shop.example.com`, **Service** `web`, which is the name of the service in your compose file, and **Port** `3000`, the port the web service listens on inside its container. All three are needed at once; if one is missing, you get the message "domain requires service and port". Save and press **Deploy now**.

Behind the scenes the agent runs one Caddy container called `haetae-caddy` on the host and tells it to forward the domain to your service. Caddy fetches and renews the certificate on its own. When the deploy is healthy the result reads `https://shop.example.com`.

## Be aware

Certificates from a public authority such as Let's Encrypt have not been tried in the Haetae tests, which used Caddy's internal authority. If the first certificate on your real domain does not appear, that is the most likely place for a surprise. See [No HTTPS](../problems/#no-https-on-a-deployed-app). A certificate that is close to expiry shows up as a problem on the host's **Security** tab.

If you do not give a domain, the result lists the published ports instead, as in `http://<address>:8088/` for the example app.
