from __future__ import annotations

import argparse
from datetime import date
import json
import sys
from typing import Sequence

from pydantic import ValidationError
from sqlalchemy import text
from sqlalchemy.exc import SQLAlchemyError
from sqlalchemy.orm import Session

from paper_hub.config import get_settings
from paper_hub.connectors.openalex import (
    OPENALEX_MAX_RESULTS,
    OpenAlexConnector,
    OpenAlexError,
)
from paper_hub.db import build_engine
from paper_hub.ingestion import IngestionService, IngestionSummary


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="paper-hub")
    subparsers = parser.add_subparsers(dest="command", required=True)
    sync_parser = subparsers.add_parser(
        "sync-openalex",
        help="Fetch and ingest scoped OpenAlex works",
    )
    sync_parser.add_argument("--query", required=True)
    sync_parser.add_argument("--from-date", type=_iso_date)
    sync_parser.add_argument(
        "--max-results",
        type=_bounded_max_results,
        default=100,
    )
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.command != "sync-openalex":
        raise AssertionError(f"unsupported command: {args.command}")

    try:
        settings = get_settings()
    except ValidationError as exc:
        invalid_fields = {
            str(error["loc"][0])
            for error in exc.errors()
            if error.get("loc")
        }
        if (
            "openalex_contact_email" in invalid_fields
            and "database_url" not in invalid_fields
        ):
            message = (
                "configuration error: OPENALEX_CONTACT_EMAIL must be "
                "a valid email address"
            )
        else:
            message = (
                "configuration error: DATABASE_URL must be configured with "
                "a postgresql+psycopg URL"
            )
        print(message, file=sys.stderr)
        return 2
    if settings.openalex_contact_email is None:
        print(
            "configuration error: OPENALEX_CONTACT_EMAIL is required",
            file=sys.stderr,
        )
        return 2

    summary = IngestionSummary()
    engine = build_engine(settings)
    try:
        with engine.connect() as connection:
            connection.execute(text("SELECT 1"))
        with OpenAlexConnector(
            contact_email=settings.openalex_contact_email,
            api_key=settings.openalex_api_key,
        ) as connector:
            with Session(engine) as session:
                try:
                    for record in connector.fetch_records(
                        query=args.query,
                        from_date=args.from_date,
                        max_results=args.max_results,
                    ):
                        try:
                            with session.begin_nested():
                                result = IngestionService(session).ingest(record)
                            summary.add(result)
                        except Exception as exc:
                            summary.failed += 1
                            print(
                                "record failed "
                                f"{record.source}:{record.source_record_id}: "
                                f"{exc.__class__.__name__}: {exc}",
                                file=sys.stderr,
                            )
                    session.commit()
                except OpenAlexError as exc:
                    session.rollback()
                    summary.failed += 1
                    print(str(exc), file=sys.stderr)
    except SQLAlchemyError as exc:
        summary.failed += 1
        print(
            "database operation failed: "
            f"{exc.__class__.__name__}",
            file=sys.stderr,
        )
    finally:
        engine.dispose()

    print(json.dumps(summary.as_dict(), sort_keys=True))
    return 0 if summary.failed == 0 else 1


def _iso_date(value: str) -> date:
    try:
        return date.fromisoformat(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError(
            "--from-date must use YYYY-MM-DD"
        ) from exc


def _bounded_max_results(value: str) -> int:
    try:
        parsed = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError(
            "--max-results must be an integer"
        ) from exc
    if not 1 <= parsed <= OPENALEX_MAX_RESULTS:
        raise argparse.ArgumentTypeError(
            "--max-results must be between 1 and "
            f"{OPENALEX_MAX_RESULTS}"
        )
    return parsed


if __name__ == "__main__":
    raise SystemExit(main())
