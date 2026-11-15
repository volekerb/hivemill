// Generates a throwaway P-256 key (pkcs8 PEM, mode 0600) in a fresh temp dir. No key is ever committed.
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

export function makeFakeKey() {
  const { privateKey, publicKey } = crypto.generateKeyPairSync("ec", { namedCurve: "prime256v1" });
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "asc-fakekey-"));
  const keyPath = path.join(dir, "AuthKey_FAKEKEY123.p8");
  const pem = privateKey.export({ type: "pkcs8", format: "pem" });
  fs.writeFileSync(keyPath, pem, { mode: 0o600 });
  return { dir, keyPath, pem, publicKey, keyId: "FAKEKEY123", issuerId: "00000000-aaaa-bbbb-cccc-111111111111" };
}
