from collections.abc import Generator

from fastapi import Depends
from sqlalchemy import text
from sqlalchemy.orm import Session

from paper_hub.db import get_session


def get_public_read_session(
    session: Session = Depends(get_session),
) -> Generator[Session, None, None]:
    session.execute(
        text(
            "SET TRANSACTION ISOLATION LEVEL "
            "REPEATABLE READ READ ONLY"
        )
    )
    yield session
