use serde_json::{Value, json};
use std::env;
use std::error::Error;
use std::fs;

fn main() -> Result<(), Box<dyn Error>> {
    let event_path = env::var("WERKT_EVENT_PATH")?;
    let result_path = env::var("WERKT_RESULT_PATH")?;
    let event: Value = serde_json::from_slice(&fs::read(event_path)?)?;

    println!(
        "{}",
        json!({ "message": "Rust received an event", "eventId": event["id"] })
    );
    let result = json!({
        "language": "rust",
        "trigger": event["trigger"],
        "received": event["data"]
    });
    fs::write(result_path, serde_json::to_vec(&result)?)?;
    Ok(())
}
