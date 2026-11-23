from aegismesh.v1 import common_pb2 as _common_pb2
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from typing import ClassVar as _ClassVar, Optional as _Optional

DESCRIPTOR: _descriptor.FileDescriptor

class TriageInput(_message.Message):
    __slots__ = ("tenant_id", "payload")
    TENANT_ID_FIELD_NUMBER: _ClassVar[int]
    PAYLOAD_FIELD_NUMBER: _ClassVar[int]
    tenant_id: str
    payload: bytes
    def __init__(self, tenant_id: _Optional[str] = ..., payload: _Optional[bytes] = ...) -> None: ...

class TriageResult(_message.Message):
    __slots__ = ("tenant_id", "payload")
    TENANT_ID_FIELD_NUMBER: _ClassVar[int]
    PAYLOAD_FIELD_NUMBER: _ClassVar[int]
    tenant_id: str
    payload: bytes
    def __init__(self, tenant_id: _Optional[str] = ..., payload: _Optional[bytes] = ...) -> None: ...

class DuplicateResult(_message.Message):
    __slots__ = ("tenant_id", "payload")
    TENANT_ID_FIELD_NUMBER: _ClassVar[int]
    PAYLOAD_FIELD_NUMBER: _ClassVar[int]
    tenant_id: str
    payload: bytes
    def __init__(self, tenant_id: _Optional[str] = ..., payload: _Optional[bytes] = ...) -> None: ...
