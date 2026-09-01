"""Reusable, dependency-free connectors for Werkt automation packages."""

from .base import ConnectorError, ConnectorField, ConnectorSpec, DownloadRequest, HTTPClient, HTTPResponse, Transport, UrllibTransport
from .calendar import CalendarEvent, ICalendar
from .command import CommandResult, CommandRunner
from .google_sheets import GoogleServiceAccount, GoogleSheets
from .ntfy import Ntfy
from .oauth2 import OAuth2ClientCredentials
from .openai import OpenAI
from .zoom import Zoom

BUILTIN_CONNECTORS = {connector.spec.id: connector.spec for connector in (Zoom, OpenAI, GoogleSheets, Ntfy)}

__all__ = [
    "BUILTIN_CONNECTORS", "CalendarEvent", "CommandResult", "CommandRunner", "ConnectorError", "ConnectorField", "ConnectorSpec",
    "DownloadRequest", "GoogleServiceAccount", "GoogleSheets", "HTTPClient", "HTTPResponse",
    "ICalendar", "Ntfy", "OAuth2ClientCredentials", "OpenAI", "Transport",
    "UrllibTransport", "Zoom",
]
