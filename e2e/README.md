# End-to-end testing

This package define [local](local) and [cloud](cloud) e2e test cases
The first use local docker mysql, the latter uses mysql and BigQuery


### Prerequisites

1. Install latest [endly](https://github.com/viant/endly/releases/tag/v0.54.0)
Copy endly to /usr/local/bin

2. Set endly [credentials](https://github.com/viant/endly/tree/master/doc/secrets) 
- local SSH
- mysql
3. Install [secrets manager](https://github.com/viant/scy/releases)

### Running test

1. Running (init,build,test)

```bash
cd local
endly
```

2. To build datly binary and run test use the following

```bash
cd local
endly -t=build,test
```

2. To run specific test case only

```bash
cd local
endly -t=test -i=uri_param
```


### Generating custom Authorization header with JWT claims
Some test use manually  OAuth security Authorization Bearer  token, you can sign JWT claims with the following command

```bash
echo '{"user_id":123,"email":"dev@test.me"}' > claims.json
scy -m=singJWT -s=claims.json -r='<datly_root>/e2e/local/jwt/public.enc|blowfish://default'
```
