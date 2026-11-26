// Package password hashes and checks passwords with Argon2id, the algorithm that OWASP and
// RFC 9106 recommend.
//
// A hash is a single string in the PHC format, such as
// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>, that carries its own salt and parameters, so it
// can be stored in one column and checked later even when the settings have changed.
//
//	hash, err := hasher.Hash(ctx, "correct horse battery staple")
//	ok, upgrade, err := hasher.Verify(ctx, attempt, hash)
//
// When upgrade is true the password is right but the hash was made with weaker settings than
// the current ones: hash the attempt again and store the new hash.
package password
