use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::env;
use std::error::Error;
use std::fs;

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Event {
    pub id: String,
    pub occurred_at: String,
    pub received_at: String,
    pub trigger: Value,
    pub data: Value,
    #[serde(default)]
    pub metadata: Value,
}

#[derive(Debug)]
pub struct Context {
    pub automation_id: String,
    pub revision_id: String,
    pub run_id: String,
}

impl Context {
    fn from_environment() -> Result<Self, Box<dyn Error>> {
        Ok(Self {
            automation_id: env::var("WERKT_AUTOMATION_ID")?,
            revision_id: env::var("WERKT_REVISION_ID")?,
            run_id: env::var("WERKT_RUN_ID")?,
        })
    }

    pub fn log(&self, message: &str) {
        println!(
            "{}",
            serde_json::json!({ "message": message, "runId": self.run_id })
        );
    }
}

pub fn execute<F, T>(handler: F) -> Result<(), Box<dyn Error>>
where
    F: FnOnce(Event, Context) -> Result<T, Box<dyn Error>>,
    T: Serialize,
{
    let event_path = env::var("WERKT_EVENT_PATH")?;
    let result_path = env::var("WERKT_RESULT_PATH")?;
    let event: Event = serde_json::from_slice(&fs::read(event_path)?)?;
    let result = handler(event, Context::from_environment()?)?;
    fs::write(result_path, serde_json::to_vec(&result)?)?;
    Ok(())
}
