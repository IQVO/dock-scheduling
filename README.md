# dock-scheduling

Dock Scheduling (WMS-tier Supporting bounded context of the yard domain): Owns the door calendar of every site: every booked time window on every dock door, inbound and outbound, from booking through arrival to door release, the no-double-booking rule, door opening hours and closures, ETA-driven risk and no-shows.

## Disclaimer

This project is a domain-driven design study. It is not associated with, endorsed by or affiliated with any company, and it uses no company's code, data, logos or trademarks. All names, figures and examples are illustrative or synthetic.

Status: under construction. The design is proposed in the yard instance of
[warehouse-docs](https://iqvo.github.io/warehouse-docs/yard/contexts/dock-scheduling/); `yard/model/model.yaml` there is the single source of truth.

Runtime: Go, hexagonal, real PostgreSQL and Kafka only (`DATABASE_URL`, `KAFKA_BROKERS` are mandatory), CloudEvents 1.0 on every message
(`com.warehouse.wms.dock-scheduling.<entity>.<Event>`), transactional outbox.

See `docs/adr/` for decisions.
