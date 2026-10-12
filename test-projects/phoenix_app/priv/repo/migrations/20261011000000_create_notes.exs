defmodule PhoenixApp.Repo.Migrations.CreateNotes do
  use Ecto.Migration

  def change do
    # Reproduces a migration that fails on real data. The composite stack's
    # bad-migration profile sets BROKEN_MIGRATION=1; creating the file
    # .data/migration-fixed stands for fixing the data.
    if System.get_env("BROKEN_MIGRATION") == "1" and not File.exists?(".data/migration-fixed") do
      raise ~s(column "owner_ref" of relation "notes" contains null values)
    end

    create table(:notes) do
      add :body, :text
      timestamps()
    end
  end
end
