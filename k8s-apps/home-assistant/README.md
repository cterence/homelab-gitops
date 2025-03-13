# home-assistant

## Sequence update for DB migration

During the migration from the truecharts managed CNPG cluster to the official chart, logical replication was used to replicate data between the old cluster (PG16) and the new cluster (PG17).

Home-assistant uses sequences to increment ID columns in all tables (ex: `event_data_data_id_seq` sequence for the `data_id` column in `event_data` table), which needed to be updated before the switchover.

Not doing will result in `Key (id)=(<number>) already exists.` errors when home-assistant tries to insert new data.

Steps:


- Easy and better: use the `kubectl cnpg` plugin to run: `kubectl cnpg subscription sync-sequences <destination_cluster> --subscription=<subscription_name>`

Hard:
- Exec in the new cluster pod and run `psql home-assistant`
- Paste the following script without executing it:
  ```sql
  DO $$
  DECLARE
      seq RECORD;
      seq_max BIGINT;
  BEGIN
      FOR seq IN
  SELECT
      n.nspname AS schema_name,
      s.relname AS sequence_name,
      t.relname AS table_name,
      a.attname AS column_name
  FROM pg_class s
  JOIN pg_namespace n ON n.oid = s.relnamespace
  JOIN pg_depend d ON d.objid = s.oid
  JOIN pg_class t ON t.oid = d.refobjid
  JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = d.refobjsubid
  WHERE s.relkind = 'S'
      LOOP
          EXECUTE format('SELECT COALESCE(MAX(%I), 0) FROM %I.%I',
                          seq.column_name, seq.schema_name, seq.table_name)
          INTO seq_max;

          IF seq_max IS NOT NULL THEN
              RAISE NOTICE 'Updating sequence %.%s to at least %', seq.schema_name, seq.sequence_name, seq_max;
              EXECUTE format('ALTER SEQUENCE %I.%I RESTART WITH %s', seq.schema_name, seq.sequence_name, seq_max + 1);
          END IF;
      END LOOP;
  END $$;
  ```
- Update database connection URI and restart home-assistant pod
- Before the pod restarts, run the script which should generate a similar output:

    ```shell
    home-assistant=# // execute script
    NOTICE:  Updating sequence public.event_data_data_id_seqs to at least 1572
    NOTICE:  Updating sequence public.event_types_event_type_id_seqs to at least 25
    NOTICE:  Updating sequence public.events_event_id_seqs to at least 42432
    NOTICE:  Updating sequence public.recorder_runs_run_id_seqs to at least 57
    NOTICE:  Updating sequence public.schema_changes_change_id_seqs to at least 2
    NOTICE:  Updating sequence public.state_attributes_attributes_id_seqs to at least 2059483
    NOTICE:  Updating sequence public.states_meta_metadata_id_seqs to at least 118
    NOTICE:  Updating sequence public.states_state_id_seqs to at least 5725883
    NOTICE:  Updating sequence public.statistics_id_seqs to at least 19541
    NOTICE:  Updating sequence public.statistics_meta_id_seqs to at least 13
    NOTICE:  Updating sequence public.statistics_runs_run_id_seqs to at least 26398
    NOTICE:  Updating sequence public.statistics_short_term_id_seqs to at least 233786
    ```

- Home-assistant will boot, connect to the new cluster and happily insert new data without issue!
