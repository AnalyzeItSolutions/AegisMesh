from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable
from typing import ClassVar as _ClassVar, Optional as _Optional

DESCRIPTOR: _descriptor.FileDescriptor

class Tenants(_message.Message):
    __slots__ = ("id", "name", "created_at", "tenant_id")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    TENANT_ID_FIELD_NUMBER: _ClassVar[int]
    id: str
    name: str
    created_at: int
    tenant_id: str
    def __init__(self, id: _Optional[str] = ..., name: _Optional[str] = ..., created_at: _Optional[int] = ..., tenant_id: _Optional[str] = ...) -> None: ...

class Incidents(_message.Message):
    __slots__ = ("id", "kind", "severity", "location", "h3_cell", "embedding", "status", "reported_at", "tenant_id")
    ID_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    SEVERITY_FIELD_NUMBER: _ClassVar[int]
    LOCATION_FIELD_NUMBER: _ClassVar[int]
    H3_CELL_FIELD_NUMBER: _ClassVar[int]
    EMBEDDING_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    REPORTED_AT_FIELD_NUMBER: _ClassVar[int]
    TENANT_ID_FIELD_NUMBER: _ClassVar[int]
    id: str
    kind: str
    severity: str
    location: str
    h3_cell: int
    embedding: _containers.RepeatedScalarFieldContainer[float]
    status: str
    reported_at: int
    tenant_id: str
    def __init__(self, id: _Optional[str] = ..., kind: _Optional[str] = ..., severity: _Optional[str] = ..., location: _Optional[str] = ..., h3_cell: _Optional[int] = ..., embedding: _Optional[_Iterable[float]] = ..., status: _Optional[str] = ..., reported_at: _Optional[int] = ..., tenant_id: _Optional[str] = ...) -> None: ...

class HazardZones(_message.Message):
    __slots__ = ("id", "polygon", "kind", "active", "tenant_id")
    ID_FIELD_NUMBER: _ClassVar[int]
    POLYGON_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    ACTIVE_FIELD_NUMBER: _ClassVar[int]
    TENANT_ID_FIELD_NUMBER: _ClassVar[int]
    id: str
    polygon: str
    kind: str
    active: bool
    tenant_id: str
    def __init__(self, id: _Optional[str] = ..., polygon: _Optional[str] = ..., kind: _Optional[str] = ..., active: _Optional[bool] = ..., tenant_id: _Optional[str] = ...) -> None: ...

class Units(_message.Message):
    __slots__ = ("id", "callsign", "capacity", "location", "status", "tenant_id")
    ID_FIELD_NUMBER: _ClassVar[int]
    CALLSIGN_FIELD_NUMBER: _ClassVar[int]
    CAPACITY_FIELD_NUMBER: _ClassVar[int]
    LOCATION_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    TENANT_ID_FIELD_NUMBER: _ClassVar[int]
    id: str
    callsign: str
    capacity: int
    location: str
    status: str
    tenant_id: str
    def __init__(self, id: _Optional[str] = ..., callsign: _Optional[str] = ..., capacity: _Optional[int] = ..., location: _Optional[str] = ..., status: _Optional[str] = ..., tenant_id: _Optional[str] = ...) -> None: ...

class TelemetryFrame(_message.Message):
    __slots__ = ("tenant_id", "unit_id", "lat", "lon", "h3_cell", "hlc")
    TENANT_ID_FIELD_NUMBER: _ClassVar[int]
    UNIT_ID_FIELD_NUMBER: _ClassVar[int]
    LAT_FIELD_NUMBER: _ClassVar[int]
    LON_FIELD_NUMBER: _ClassVar[int]
    H3_CELL_FIELD_NUMBER: _ClassVar[int]
    HLC_FIELD_NUMBER: _ClassVar[int]
    tenant_id: str
    unit_id: str
    lat: float
    lon: float
    h3_cell: int
    hlc: int
    def __init__(self, tenant_id: _Optional[str] = ..., unit_id: _Optional[str] = ..., lat: _Optional[float] = ..., lon: _Optional[float] = ..., h3_cell: _Optional[int] = ..., hlc: _Optional[int] = ...) -> None: ...

class HazardAck(_message.Message):
    __slots__ = ("tenant_id", "payload")
    TENANT_ID_FIELD_NUMBER: _ClassVar[int]
    PAYLOAD_FIELD_NUMBER: _ClassVar[int]
    tenant_id: str
    payload: bytes
    def __init__(self, tenant_id: _Optional[str] = ..., payload: _Optional[bytes] = ...) -> None: ...

class HazardPolygon(_message.Message):
    __slots__ = ("tenant_id", "payload")
    TENANT_ID_FIELD_NUMBER: _ClassVar[int]
    PAYLOAD_FIELD_NUMBER: _ClassVar[int]
    tenant_id: str
    payload: bytes
    def __init__(self, tenant_id: _Optional[str] = ..., payload: _Optional[bytes] = ...) -> None: ...
