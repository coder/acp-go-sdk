// Writes $OUT_DIR/decode.rs: one match arm per $defs name of the checkout's
// schema/schema.json and schema/schema.unstable.json, each decoding and
// re-encoding the value as the crate type of that name. A def that the crate
// has no type for fails the build, so a crate pin that is older than the
// schema shows up here.
use std::{collections::BTreeSet, env, fs, path::PathBuf};

// Defs whose crate type is not v1::<def>.
const PATHS: &[(&str, &str)] = &[(
    "ProtocolVersion",
    "agent_client_protocol_schema::ProtocolVersion",
)];

fn main() {
    let manifest = PathBuf::from(env::var("CARGO_MANIFEST_DIR").unwrap());
    let schema = manifest.join("../../../../schema");
    let mut names = BTreeSet::new();
    for file in ["schema.json", "schema.unstable.json"] {
        let path = schema.join(file);
        println!("cargo:rerun-if-changed={}", path.display());
        let text = fs::read_to_string(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()));
        let doc: serde_json::Value = serde_json::from_str(&text).unwrap();
        let defs = doc["$defs"].as_object().expect("$defs");
        names.extend(defs.keys().cloned());
    }
    let mut out = String::from("match def {\n");
    for n in &names {
        let path = match PATHS.iter().find(|(def, _)| def == n) {
            Some((_, path)) => path.to_string(),
            None => format!("v1::{n}"),
        };
        out += &format!("    {n:?} => round_trip::<{path}>(value),\n");
    }
    out += "    _ => Err(format!(\"NO-TYPE {def}\")),\n}\n";
    fs::write(
        PathBuf::from(env::var("OUT_DIR").unwrap()).join("decode.rs"),
        out,
    )
    .unwrap();
}
