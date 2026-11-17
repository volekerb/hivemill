// Node module hook for the tests: the mod's `import ... from "herdr"` resolves to the stub next to this file.
export async function resolve(specifier, context, next) {
  if (specifier === "herdr") return { url: new URL("./herdr-stub.mjs", import.meta.url).href, shortCircuit: true };
  return next(specifier, context);
}
