#!/usr/bin/env bash
# proves: the pip frostroot pins refuses a server whose authority it does not know, and fetches from it when given the authority with --cert, as [certificates] and --ca-bundle do, or told --trusted-host, as --insecure does
# needs: the network (once, for the pinned pip), python3, openssl, curl
# takes: about a minute
#
# A private authority, a certificate for localhost signed by it, and a local
# HTTPS server holding one wheel: the pinned pip's own. The pinned pip runs
# from its wheel, which Python's zip import allows, so the host needs no
# virtual environment support. Its URL and checksum are read out of
# internal/builder/python.go, where PinnedPip is, so this checks the pip
# frostroot installs today.
source "$(dirname "$0")/lib.sh"

e2e_begin pip-trust
e2e_require python3 openssl curl
pythonGo=$e2eRepo/internal/builder/python.go
pinned() { # FIELD: PinnedPip's value for it
	sed -n "/^var PinnedPip = /,/^}/s/^[[:space:]]*$1:[[:space:]]*\"\\(.*\\)\",\$/\\1/p" "$pythonGo"
}
pipURL=$(pinned URL)
pipSHA=$(pinned SHA256)
pipVersion=$(pinned Version)
if [ -z "$pipURL" ] || [ -z "$pipSHA" ] || [ -z "$pipVersion" ]; then
	e2e_abort "could not read PinnedPip out of $pythonGo"
fi
mkdir -p "$LAB/serve"
wheel=$LAB/serve/$(basename "$pipURL")
curl -fsSL -o "$wheel" "$pipURL" || e2e_abort "could not download $pipURL"
e2e_check "the downloaded pip is the file frostroot pins" test "$(e2e_sha "$wheel")" = "$pipSHA"

e2e_certificate "frostroot e2e Private Root CA" "$LAB/ca.key" "$LAB/ca.pem" || e2e_abort "openssl failed"
openssl req -newkey rsa:2048 -nodes -subj "/CN=localhost" -keyout "$LAB/server.key" -out "$LAB/server.csr" > /dev/null 2>&1 ||
	e2e_abort "openssl could not make the server's key"
printf '%s\n' "subjectAltName=DNS:localhost" "basicConstraints=critical,CA:FALSE" \
	"keyUsage=critical,digitalSignature,keyEncipherment" "extendedKeyUsage=serverAuth" > "$LAB/server.ext"
openssl x509 -req -in "$LAB/server.csr" -CA "$LAB/ca.pem" -CAkey "$LAB/ca.key" -CAcreateserial -days 2 \
	-extfile "$LAB/server.ext" -out "$LAB/server.pem" > /dev/null 2>&1 || e2e_abort "openssl could not sign the server's certificate"

python3 - "$LAB/serve" "$LAB/server.pem" "$LAB/server.key" "$LAB/port" > "$LAB/server.log" 2>&1 << 'PY' &
import functools, http.server, os, ssl, sys

directory, certificate, key, port_file = sys.argv[1:5]
handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=directory)
server = http.server.HTTPServer(("127.0.0.1", 0), handler)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(certificate, key)
server.socket = context.wrap_socket(server.socket, server_side=True)
with open(port_file + ".tmp", "w") as out:
    out.write(str(server.server_address[1]))
os.rename(port_file + ".tmp", port_file)
server.serve_forever()
PY
server=$!
trap 'kill "$server" 2> /dev/null' EXIT
for _ in $(seq 50); do
	[ -s "$LAB/port" ] && break
	sleep 0.2
done
[ -s "$LAB/port" ] || e2e_abort "the HTTPS server did not start; see $LAB/server.log"
port=$(cat "$LAB/port")

# What frostroot's Python step does before pip runs: nothing of this host's
# pip configuration or certificate paths may decide the outcome.
while IFS= read -r variable; do
	unset "$variable"
done < <(env | sed -n 's/^\(PIP_[A-Za-z0-9_]*\)=.*/\1/p')
unset REQUESTS_CA_BUNDLE CURL_CA_BUNDLE SSL_CERT_FILE SSL_CERT_DIR
export PIP_CONFIG_FILE=/dev/null

# download LABEL [PIP-OPTION...] asks the server for the pinned pip.
download() {
	local label=$1
	shift
	e2e_run "$label" "$LAB" python3 "$wheel/pip" download --no-index --find-links "https://localhost:$port/" \
		--no-deps --retries 0 --timeout 15 --disable-pip-version-check --dest "$LAB/out-$label" "$@" "pip==$pipVersion"
}

download refuse
e2e_check "pip refuses a server no authority it knows vouches for" test "$e2eStatus" -ne 0
e2e_check "and says the certificate is why" grep -q "CERTIFICATE_VERIFY_FAILED" "$LAB/refuse.out" "$LAB/refuse.err"

download cert --cert "$LAB/ca.pem"
e2e_expect_status 0 cert
e2e_check "with --cert it fetches the wheel intact" test "$(e2e_sha "$LAB/out-cert/$(basename "$wheel")")" = "$pipSHA"

download trusted --trusted-host "localhost:$port"
e2e_expect_status 0 trusted
e2e_check "with --trusted-host it fetches the wheel intact" test "$(e2e_sha "$LAB/out-trusted/$(basename "$wheel")")" = "$pipSHA"
e2e_end
