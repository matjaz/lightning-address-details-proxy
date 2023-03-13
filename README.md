# lightning-address-details-proxy

This proxy exists to simplify requests to lightning address providers.

- Many ln addresses don't support CORS, which means fetching the data directly in a browser environment will not always work.
- Two requests are required to retrieve lnurlp and keysend data for a lightning address. The proxy will do these for you with a single request.

# API

## Getting keysend and LNURLp info

`GET /lightning-address-details?ln=<lightning_address>`

### Example

GET https://lnaddressproxy.getalby.com/lightning-address-details?ln=hello@getalby.com

## Requesting a LNURLp invoice

`GET /generate-invoice?ln=<lightning_address>&amount=<amount_in_millisats>&comment=<http_encoded_comment>`

Please refer to the [https://github.com/lnurl/luds](LUDs) (LUD-06, LUD-12) etc for more information on what parameters can be passed.

### Example

GET https://lnaddressproxy.getalby.com/generate-invoice?ln=hello@getalby.com&amount=1000&comment=Hello%20Alby!

---

This proxy is used by [Alby Tools](https://github.com/getAlby/alby-tools)
