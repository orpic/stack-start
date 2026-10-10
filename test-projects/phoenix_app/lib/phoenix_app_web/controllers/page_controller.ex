defmodule PhoenixAppWeb.PageController do
  use PhoenixAppWeb, :controller

  # The public URL the stack was given (e.g. a tunnel captured by stackstart).
  def tunnel(conn, _params) do
    text(conn, System.get_env("TUNNEL_URL") || "none")
  end

  def home(conn, _params) do
    render(conn, :home)
  end
end
