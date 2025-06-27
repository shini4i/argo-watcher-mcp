#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""
An interactive, class-based command-line chat application for the ArgoWatcherMCP server.

This script uses a structured, object-oriented approach and leverages the 'click'
library for robust command-line interaction and error handling.
"""

import asyncio
import json
import os
from urllib.parse import urlencode
from typing import Any, Dict, List

from mcp.client.session import ClientSession
from mcp.client.sse import sse_client

# OpenAI imports
from openai import AsyncOpenAI, OpenAIError
from openai.types.chat import (
    ChatCompletionMessageParam,
    ChatCompletionToolParam,
)

# CLI and Output imports
import click
from rich.console import Console
from rich.markdown import Markdown
from icecream import ic

# --- Configuration ---
BASE_URL = os.getenv("MCP_SERVER_URL", "http://localhost:8000")
SSE_INIT_PATH = "/sse"


class ArgoWatcherClient:
    """
    A client responsible for all direct communication with the ArgoWatcher MCP server.
    """
    def __init__(self, base_url: str, sse_path: str):
        self.base_url = base_url
        self.sse_path = sse_path

    def _get_init_url(self) -> str:
        """Constructs the initial URL required to start an MCP session."""
        init_command = {"mcp_version": "1.0", "method": "mcp.discovery.list_tools"}
        query_params = urlencode({"p": json.dumps(init_command)})
        return f"{self.base_url.rstrip('/')}/{self.sse_path.lstrip('/')}?{query_params}"

    async def discover_tools(self) -> List[ChatCompletionToolParam]:
        """Connects to the MCP server to discover the available tools."""
        init_url = self._get_init_url()
        openai_tools: List[ChatCompletionToolParam] = []
        try:
            async with sse_client(url=init_url) as (read_stream, write_stream):
                session = ClientSession(read_stream=read_stream, write_stream=write_stream)
                async with session:
                    await session.initialize()
                    mcp_tools_response = await session.list_tools()
                    if hasattr(mcp_tools_response, 'tools'):
                        for tool in mcp_tools_response.tools:
                            openai_tools.append({
                                "type": "function",
                                "function": {
                                    "name": tool.name,
                                    "description": tool.description,
                                    "parameters": tool.inputSchema,
                                },
                            })
            return openai_tools
        except Exception as e:
            # Let exceptions bubble up to be handled by the main CLI function.
            raise click.ClickException(f"Error discovering tools: {e}")

    async def execute_tool(self, tool_name: str, tool_args: Dict[str, Any]) -> str:
        """Establishes a new connection to execute a single tool call."""
        ic(f"🤖 Calling tool {tool_name} with arguments:", tool_args)
        init_url = self._get_init_url()
        try:
            async with sse_client(url=init_url) as (read_stream, write_stream):
                session = ClientSession(read_stream=read_stream, write_stream=write_stream)
                async with session:
                    await session.initialize()
                    mcp_tool_result = await session.call_tool(name=tool_name, arguments=tool_args)
                    if mcp_tool_result and mcp_tool_result.content:
                        all_results = [json.loads(item.text) for item in mcp_tool_result.content if hasattr(item, 'text')]
                        return json.dumps(all_results)
                    return "[]"
        except Exception as e:
            # Raise a specific, user-friendly error.
            raise click.ClickException(f"Error executing MCP tool '{tool_name}': {e}")


class ChatManager:
    """
    Manages the interactive chat session, conversation history, and LLM interaction.
    """
    def __init__(self, mcp_client: ArgoWatcherClient, llm_client: AsyncOpenAI, console: Console):
        self.mcp_client = mcp_client
        self.llm_client = llm_client
        self.console = console
        self.messages: List[ChatCompletionMessageParam] = []
        self.tools: List[ChatCompletionToolParam] = []

    async def initialize(self):
        """Discover tools before starting the chat."""
        self.console.print("Discovering tools from MCP server...")
        self.tools = await self.mcp_client.discover_tools()
        if not self.tools:
            # Raise an exception to be handled by the CLI error handler.
            raise click.ClickException("No tools found on the server. Cannot start chat.")
        self.console.print("[bold green]✔ Tools discovered successfully.[/bold green]")


    async def handle_tool_calls(self, response_message):
        """Processes tool calls requested by the LLM."""
        ic("🧠 LLM decided to call a tool...")
        for tool_call in response_message.tool_calls:
            function_name = tool_call.function.name
            function_args = json.loads(tool_call.function.arguments)
            ic("LLM wants to call:", function_name, function_args)

            function_response_str = await self.mcp_client.execute_tool(
                tool_name=function_name,
                tool_args=function_args,
            )

            ic("Raw response from tool:", function_response_str)
            self.messages.append(
                {
                    "tool_call_id": tool_call.id,
                    "role": "tool",
                    "name": function_name,
                    "content": function_response_str,
                }
            )

        with self.console.status("[bold blue]Summarizing tool results...[/bold blue]", spinner="dots"):
            second_response = await self.llm_client.chat.completions.create(
                model="gpt-4o",
                messages=self.messages,
            )
        final_answer = second_response.choices[0].message.content
        self.messages.append(second_response.choices[0].message)
        return final_answer

    async def start_chat(self):
        """The main interactive chat loop."""
        await self.initialize()
        self.console.print("\n[bold]Welcome to the Argo Watcher AI Assistant![/bold]")
        self.console.print("Type your questions below, or type 'exit' or 'quit' to end the session.")

        while True:
            try:
                prompt = self.console.input("[bold yellow]You: [/bold yellow]")
                if prompt.lower() in ["exit", "quit"]:
                    self.console.print("[bold]Goodbye![/bold]")
                    break

                self.messages.append({"role": "user", "content": prompt})

                with self.console.status("[bold blue]Thinking...[/bold blue]", spinner="dots"):
                    response = await self.llm_client.chat.completions.create(
                        model="gpt-4o",
                        messages=self.messages,
                        tools=self.tools,
                        tool_choice="auto",
                    )
                    response_message = response.choices[0].message
                    self.messages.append(response_message)

                if response_message.tool_calls:
                    final_answer = await self.handle_tool_calls(response_message)
                else:
                    final_answer = response_message.content

                self.console.print("\n[bold green]Assistant:[/bold green]")
                self.console.print(Markdown(final_answer or "Sorry, I couldn't generate a response."))
                self.console.print("-" * 50)

            except (KeyboardInterrupt, EOFError):
                self.console.print("\n[bold]Goodbye![/bold]")
                break
            except OpenAIError as e:
                self.console.print(f"\n[bold red]An OpenAI API error occurred: {e}[/bold red]")
                # Remove the last user message to allow retrying
                self.messages.pop()
            except Exception as e:
                self.console.print(f"\n[bold red]An unexpected error occurred during the chat: {e}[/bold red]")


@click.command()
@click.option('--debug', is_flag=True, help="Enable debug output with icecream.")
def cli(debug):
    """An interactive CLI to chat with the Argo Watcher MCP server via an LLM."""
    if not debug:
        ic.disable()

    # --- Use click for cleaner error handling ---
    if not os.getenv("OPENAI_API_KEY"):
        raise click.ClickException("The OPENAI_API_KEY environment variable is not set.")

    try:
        # Initialize clients and managers
        llm_client = AsyncOpenAI()
        console = Console()
        mcp_client = ArgoWatcherClient(base_url=BASE_URL, sse_path=SSE_INIT_PATH)
        chat_manager = ChatManager(mcp_client, llm_client, console)

        # Run the main chat loop
        asyncio.run(chat_manager.start_chat())

    except click.ClickException:
        # Re-raise to let click handle the exit and message.
        raise
    except Exception as e:
        # Catch any other unexpected startup errors.
        raise click.ClickException(f"Failed to start the application: {e}")


if __name__ == "__main__":
    cli()
