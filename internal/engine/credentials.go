package engine

import (
	"crypto/sha1"
	"encoding/base64"
)

// adminCredentials returns the basic-auth user and htpasswd-format hashed
// password guarding the dashboard and metrics routes, from
// DYNO_PROXY_ADMIN_USER / DYNO_PROXY_ADMIN_PASSWORD. ok is false unless both are
// set, in which case those routes are not served.
func adminCredentials(getenv func(string) string) (user, hashedPassword string, ok bool) {
	user = getenv("DYNO_PROXY_ADMIN_USER")
	password := getenv("DYNO_PROXY_ADMIN_PASSWORD")
	if user == "" || password == "" {
		return "", "", false
	}
	return user, sha1Htpasswd(password), true
}

// sha1Htpasswd hashes a password in htpasswd SHA1 format ({SHA}base64) as
// produced by `htpasswd -s`.
func sha1Htpasswd(password string) string {
	h := sha1.New()
	h.Write([]byte(password))
	return "{SHA}" + base64.StdEncoding.EncodeToString(h.Sum(nil))
}
