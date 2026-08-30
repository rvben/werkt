use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::env;
use std::error::Error;
use std::fs;
use std::path::PathBuf;

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
    control_path: PathBuf,
}

impl Context {
    fn from_environment() -> Result<Self, Box<dyn Error>> {
        Ok(Self {
            automation_id: env::var("WERKT_AUTOMATION_ID")?,
            revision_id: env::var("WERKT_REVISION_ID")?,
            run_id: env::var("WERKT_RUN_ID")?,
            control_path: PathBuf::from(env::var("WERKT_CONTROL_PATH")?),
        })
    }

    pub fn log(&self, message: &str) {
        println!(
            "{}",
            serde_json::json!({ "message": message, "runId": self.run_id })
        );
    }

    pub fn defer(&self, key: &str, until: &str, data: Value) -> Result<(), Box<dyn Error>> {
        self.write_control(serde_json::json!({
            "defer": { "key": key, "until": until, "data": data }
        }))
    }

    pub fn request_approval(&self, request: ApprovalRequest) -> Result<(), Box<dyn Error>> {
        self.write_control(serde_json::json!({ "approval": request }))
    }

    fn write_control(&self, value: Value) -> Result<(), Box<dyn Error>> {
        fs::write(&self.control_path, serde_json::to_vec(&value)?)?;
        Ok(())
    }
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ApprovalRequest {
    pub key: String,
    pub title: String,
    pub description: String,
    pub expires_at: String,
    pub fields: Vec<ApprovalField>,
    pub actions: Vec<ApprovalAction>,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ApprovalField {
    pub id: String,
    pub label: String,
    #[serde(rename = "type")]
    pub field_type: String,
    pub required: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub value: Option<Value>,
    pub description: String,
    pub options: Vec<String>,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ApprovalAction {
    pub id: String,
    pub label: String,
    pub style: String,
    pub requires_fields: bool,
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
