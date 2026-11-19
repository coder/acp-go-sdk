//! Reads one case per line on stdin, `{"id":..,"def":..,"json":..}`, decodes
//! `json` as the crate type named `def`, and writes one line per case on
//! stdout: `{"id":..,"ok":true,"out":..}` with the type encoded again, or
//! `{"id":..,"ok":false,"err":..}`.
use agent_client_protocol_schema::v1;
use serde::{de::DeserializeOwned, Deserialize, Serialize};
use serde_json::{json, value::RawValue, Value};
use std::io::{self, BufRead, Write};

// json stays raw, so the crate type reads the case's own bytes: a value
// parsed into serde_json::Value first would lose a duplicate member.
#[derive(Deserialize)]
struct Case {
    id: Value,
    def: String,
    json: Box<RawValue>,
}

fn round_trip<T: DeserializeOwned + Serialize>(json: &str) -> Result<Value, String> {
    let t: T = serde_json::from_str(json).map_err(|e| e.to_string())?;
    serde_json::to_value(&t).map_err(|e| e.to_string())
}

fn decode(def: &str, value: &str) -> Result<Value, String> {
    include!(concat!(env!("OUT_DIR"), "/decode.rs"))
}

fn main() {
    let stdout = io::stdout();
    let mut out = stdout.lock();
    for line in io::stdin().lock().lines() {
        let line = line.expect("read stdin");
        if line.trim().is_empty() {
            continue;
        }
        let case: Case = serde_json::from_str(&line).expect("case is {id, def, json}");
        let result = match decode(&case.def, case.json.get()) {
            Ok(v) => json!({"id": case.id, "ok": true, "out": v}),
            Err(e) => json!({"id": case.id, "ok": false, "err": e}),
        };
        writeln!(out, "{result}").expect("write stdout");
    }
}
