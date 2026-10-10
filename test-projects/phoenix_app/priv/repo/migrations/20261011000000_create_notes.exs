defmodule PhoenixApp.Repo.Migrations.CreateNotes do
  use Ecto.Migration

  def change do
    # Reproduces a migration that fails on real data. The composite stack's
    # bad-migration profile sets BROKEN_MIGRATION=1.
    if System.get_env("BROKEN_MIGRATION") == "1" do
      raise ~s(column "organization_id" of relation "notes" contains null values)
    end

    create table(:notes) do
      add :body, :text
      timestamps()
    end
  end
end
