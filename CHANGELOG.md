# Changelog

All notable changes to this project are documented in this file. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Modules with imports, exports and global modules.
- Dependency injection through constructors, with `Provide`, `As` for interfaces and `Value`.
- Validation of the whole module graph when the application is built, reporting every missing
  dependency, cycle and declaration error at once with a hint on how to fix it.
- Application lifecycle with `Starter` and `Stopper` hooks, graceful shutdown on interrupt and
  termination signals, and `Shutdowner` to stop the application from a provider.
- `Override` to replace providers in tests, and `Resolve` to get a provider from a running
  application.
- The `config` package: typed configuration from environment variables and dotenv files.
